package backup

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

func (m *Manager) connect(ctx context.Context) (*pgx.Conn, error) {
	c, e := pgx.ParseConfig(m.cfg.DSN)
	if e != nil {
		return nil, e
	}
	c.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	return pgx.ConnectConfig(ctx, c)
}

// Credentials travel in the child environment, never argv or returned diagnostics.
func (m *Manager) command(ctx context.Context, tool string, out io.Writer, args ...string) error {
	c, e := pgx.ParseConfig(m.cfg.DSN)
	if e != nil {
		return e
	}
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "LC_ALL=C")
	cmd.Env = append(cmd.Env, "PGHOST="+c.Host, "PGPORT="+strconv.Itoa(int(c.Port)), "PGUSER="+c.User, "PGPASSWORD="+c.Password, "PGDATABASE="+c.Database)
	// Preserve libpq TLS/connection options from the original connection string.
	if tool == "pg_dump" || slices.Contains(args, "--exit-on-error") {
		u, err := url.Parse(m.cfg.DSN)
		if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
			return engineFailure(FailureDatabaseConnection, "backup DSN must be a PostgreSQL URL")
		}
		u.User = url.User(c.User)
		q := u.Query()
		q.Del("password")
		u.RawQuery = q.Encode()
		cmd.Args = append(cmd.Args, "--dbname="+u.String())
	}
	output := &observedWriter{writer: &boundedWriter{w: out, remaining: maxBytes}}
	cmd.Stdout = output
	stderr := &stderrSignals{}
	cmd.Stderr = stderr
	if e = cmd.Run(); e != nil {
		diagnostic := commandDiagnostic(tool, e, stderr)
		if ctx.Err() != nil {
			return errors.Join(ctx.Err(), diagnostic)
		}
		if output.err != nil {
			return errors.Join(output.err, diagnostic)
		}
		return diagnostic
	}
	return nil
}

type observedWriter struct {
	writer io.Writer
	err    error
}

func (w *observedWriter) Write(p []byte) (int, error) {
	n, e := w.writer.Write(p)
	if e != nil && w.err == nil {
		w.err = e
	}
	return n, e
}
func (m *Manager) preflight(ctx context.Context) error {
	c, e := m.connect(ctx)
	if e != nil {
		return classified(FailureDatabaseConnection, e)
	}
	defer c.Close(ctx)
	var version int
	var allowed bool
	if e = c.QueryRow(ctx, `SELECT current_setting('server_version_num')::int, has_database_privilege(current_database(),'CREATE') AND has_schema_privilege('public','CREATE') AND pg_has_role((SELECT nspowner FROM pg_namespace WHERE nspname='public'),'USAGE')`).Scan(&version, &allowed); e != nil {
		return e
	}
	if !allowed {
		return engineFailure(FailureDatabasePrivileges, "application role must own public and have database CREATE privilege (CREATEDB is not needed)")
	}
	var extraSchemas, extensions, largeObjects bool
	if e = c.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname NOT IN ('public','information_schema') AND nspname NOT LIKE 'pg_%'), EXISTS(SELECT 1 FROM pg_extension WHERE extname <> 'plpgsql'), EXISTS(SELECT 1 FROM pg_largeobject_metadata)`).Scan(&extraSchemas, &extensions, &largeObjects); e != nil {
		return e
	}
	if extraSchemas {
		return engineFailure(FailureDatabaseExtraSchemas, "additional non-system schemas are present")
	}
	if extensions {
		return engineFailure(FailureDatabaseExtensions, "extensions other than plpgsql are present")
	}
	if largeObjects {
		return engineFailure(FailureDatabaseLargeObjects, "PostgreSQL large objects are present")
	}
	var databaseBytes int64
	if e = c.QueryRow(ctx, `SELECT pg_database_size(current_database())`).Scan(&databaseBytes); e != nil {
		return e
	}
	files, e := inventory(m.cfg.UploadDir)
	if e != nil {
		return e
	}
	var fileBytes int64
	for _, v := range files {
		fileBytes += v.Size
	}
	if e = space(m.root, 3*databaseBytes+2*fileBytes); e != nil {
		return e
	}
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		b, e := exec.CommandContext(ctx, tool, "--version").Output()
		if e != nil {
			return classified(FailureToolUnavailable, commandDiagnostic(tool, e, &stderrSignals{}))
		}
		re := regexp.MustCompile(`\b(\d+)\.`)
		v := re.FindStringSubmatch(string(b))
		if len(v) != 2 || v[1] != strconv.Itoa(version/10000) {
			return engineFailure(FailureToolCompatibility, "PostgreSQL tools must match server major version")
		}
	}
	return space(m.root, 256<<20)
}
func (m *Manager) dump(ctx context.Context, dir string) error {
	f, e := os.OpenFile(filepath.Join(dir, "database.dump"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	e = m.command(ctx, "pg_dump", f, "--format=custom", "--compress=0", "--schema=public", "--no-owner", "--no-acl")
	if e == nil {
		e = f.Sync()
	}
	return errors.Join(e, f.Close())
}
func (m *Manager) render(ctx context.Context, dump, section, target string) error {
	f, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	var output io.Writer = f
	if section != "data" {
		output = &boundedWriter{w: f, remaining: maxManifest}
	}
	e = m.command(ctx, "pg_restore", output, "--no-owner", "--no-acl", "--section="+section, "--file=-", dump)
	if e == nil {
		e = f.Sync()
	}
	return errors.Join(e, f.Close())
}
func schemaHash(paths ...string) (string, error) {
	h := sha256.New()
	for _, p := range paths {
		f, e := os.Open(p)
		if e != nil {
			return "", e
		}
		s := bufio.NewScanner(f)
		s.Buffer(make([]byte, 65536), 16<<20)
		for s.Scan() {
			line := s.Text()
			if strings.HasPrefix(line, "--") || strings.HasPrefix(line, `\restrict `) || strings.HasPrefix(line, `\unrestrict `) || strings.TrimSpace(line) == "" {
				continue
			}
			_, _ = io.WriteString(h, line+"\n")
		}
		e = errors.Join(s.Err(), f.Close())
		if e != nil {
			return "", e
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
func (m *Manager) prepare(ctx context.Context, dir string) (string, error) {
	for _, s := range []string{"pre-data", "data", "post-data"} {
		if e := m.render(ctx, filepath.Join(dir, "database.dump"), s, filepath.Join(dir, s+".sql")); e != nil {
			return "", e
		}
	}
	if e := importData(ctx, nil, filepath.Join(dir, "data.sql")); e != nil {
		return "", e
	}
	return schemaHash(filepath.Join(dir, "pre-data.sql"), filepath.Join(dir, "post-data.sql"))
}

const sqlIdentifier = `(?:[a-z_][a-z_0-9]*|"[a-z_][a-z_0-9]*")`

var copyPattern = regexp.MustCompile(`^COPY public\.(` + sqlIdentifier + `) \((` + sqlIdentifier + `(?:, ` + sqlIdentifier + `)*)\) FROM stdin;$`)
var sequencePattern = regexp.MustCompile(`^SELECT pg_catalog\.setval\('public\.([a-z_][a-z_0-9]*)', (-?[0-9]+), (true|false)\);$`)
var restrictPattern = regexp.MustCompile(`^\\(?:un)?restrict [a-zA-Z0-9]+$`)
var settings = map[string]bool{
	"SET statement_timeout = 0;": true, "SET lock_timeout = 0;": true, "SET idle_in_transaction_session_timeout = 0;": true, "SET transaction_timeout = 0;": true, "SET client_encoding = 'UTF8';": true, "SET standard_conforming_strings = on;": true, "SELECT pg_catalog.set_config('search_path', '', false);": true, "SET check_function_bodies = false;": true, "SET xmloption = content;": true, "SET client_min_messages = warning;": true, "SET row_security = off;": true,
}

// Data SQL is never executed. Only exact COPY headers and numeric sequence
// assignments are interpreted; pgx sends COPY bytes through the wire protocol.
func importData(ctx context.Context, c *pgx.Conn, p string, references ...map[string]bool) error {
	f, e := os.Open(p)
	if e != nil {
		return e
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 65536)
	tables := map[string]bool{}
	sequences := map[string]bool{}
	for {
		line, e := readLine(r)
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return e
		}
		text := strings.TrimSuffix(line, "\n")
		if text == "" || strings.HasPrefix(text, "--") || settings[text] || restrictPattern.MatchString(text) {
			continue
		}
		if match := copyPattern.FindStringSubmatch(text); match != nil {
			match[1] = strings.Trim(match[1], `"`)
			if tables[match[1]] {
				return engineFailure(FailureArchiveFormat, "duplicate COPY table")
			}
			tables[match[1]] = true
			cr := &copyReader{r: r, pathColumn: -1, columns: len(strings.Split(match[2], ", "))}
			if len(references) > 0 {
				column := ""
				if match[1] == "files" {
					column = "file_path"
				}
				if match[1] == "company_settings" {
					column = "invoice_logo_path"
				}
				for i, v := range strings.Split(match[2], ", ") {
					if column != "" && strings.Trim(v, `"`) == column {
						cr.pathColumn = i
						cr.references = references[0]
					}
				}
			}
			if c == nil {
				_, e = io.Copy(io.Discard, cr)
			} else {
				_, e = c.PgConn().CopyFrom(ctx, cr, text)
			}
			if e != nil {
				return e
			}
			if !cr.done {
				return engineFailure(FailureArchiveFormat, "incomplete COPY")
			}
			continue
		}
		if match := sequencePattern.FindStringSubmatch(text); match != nil {
			if sequences[match[1]] {
				return engineFailure(FailureArchiveFormat, "duplicate sequence assignment")
			}
			sequences[match[1]] = true
			n, e := strconv.ParseInt(match[2], 10, 64)
			if e != nil {
				return e
			}
			if c != nil {
				_, e = c.Exec(ctx, "SELECT pg_catalog.setval($1::regclass,$2,$3)", "public."+match[1], n, match[3] == "true")
				if e != nil {
					return e
				}
			}
			continue
		}
		return engineFailure(FailureArchiveFormat, "database contains unsupported executable data statements")
	}
}
func readLine(r *bufio.Reader) (string, error) {
	var b strings.Builder
	for {
		v, e := r.ReadSlice('\n')
		if b.Len()+len(v) > 16<<20 {
			return "", engineFailure(FailureResourceLimit, "database row exceeds 16 MiB limit")
		}
		b.Write(v)
		if e == bufio.ErrBufferFull {
			continue
		}
		if e == io.EOF && b.Len() > 0 {
			return "", engineFailure(FailureArchiveFormat, "truncated database SQL")
		}
		return b.String(), e
	}
}

type copyReader struct {
	r          *bufio.Reader
	pending    string
	done       bool
	pathColumn int
	references map[string]bool
	columns    int
	rowOpen    bool
	fieldIndex int
	pathValue  strings.Builder
}

func (c *copyReader) Read(p []byte) (int, error) {
	if c.done {
		return 0, io.EOF
	}
	if c.pending == "" {
		fragment, e := c.r.ReadSlice('\n')
		if e != nil && e != bufio.ErrBufferFull {
			return 0, engineFailure(FailureArchiveFormat, "truncated COPY")
		}
		s := string(fragment)
		if !c.rowOpen && s == "\\.\n" {
			c.done = true
			return 0, io.EOF
		}
		for i := 0; i < len(s); i++ {
			ch := s[i]
			if ch == '\t' {
				c.fieldIndex++
				continue
			}
			if ch == '\n' {
				if c.columns > 0 && c.fieldIndex+1 != c.columns {
					return 0, engineFailure(FailureArchiveFormat, "COPY row column count mismatch")
				}
				if c.references != nil {
					v := c.pathValue.String()
					if v != "" && v != `\N` {
						decoded, e := decodeCopyPath(v)
						if e != nil {
							return 0, e
						}
						if !c.references[decoded] {
							return 0, engineFailure(FailureFileReferences, "database file reference missing from manifest")
						}
					}
				}
				c.fieldIndex = 0
				c.pathValue.Reset()
				continue
			}
			if c.references != nil && c.fieldIndex == c.pathColumn {
				if c.pathValue.Len() >= 8192 {
					return 0, engineFailure(FailureResourceLimit, "file path exceeds resource limit")
				}
				c.pathValue.WriteByte(ch)
			}
		}
		c.rowOpen = e == bufio.ErrBufferFull
		c.pending = s
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}
func (m *Manager) apply(ctx context.Context, trusted, data string) error {
	if e := m.advanceGeneration(); e != nil {
		return e
	}
	c, e := m.connect(ctx)
	if e != nil {
		return e
	}
	defer c.Close(ctx)
	if _, e = c.Exec(ctx, "DROP SCHEMA IF EXISTS public CASCADE"); e != nil {
		return e
	}
	if e = m.check("database-dropped"); e != nil {
		return e
	}
	// Only locally captured schema is ever supplied to a SQL interpreter.
	for _, section := range []string{"pre-data", "post-data"} {
		if section == "post-data" {
			if e = importData(ctx, c, filepath.Join(data, "data.sql")); e != nil {
				return e
			}
		}
		if e = m.command(ctx, "pg_restore", io.Discard, "--exit-on-error", "--single-transaction", "--no-owner", "--no-acl", "--section="+section, filepath.Join(trusted, "database.dump")); e != nil {
			return e
		}
		if e = m.check("database-" + section); e != nil {
			return e
		}
	}
	return nil
}
func (m *Manager) mappings(ctx context.Context) ([]pathMapping, error) {
	c, e := m.connect(ctx)
	if e != nil {
		return nil, e
	}
	defer c.Close(ctx)
	rows, e := c.Query(ctx, `SELECT file_path FROM public.files WHERE file_path <> '' UNION SELECT invoice_logo_path FROM public.company_settings WHERE invoice_logo_path <> ''`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []pathMapping
	var metadataBytes int
	for rows.Next() {
		var p string
		if e = rows.Scan(&p); e != nil {
			return nil, e
		}
		abs, e := filepath.Abs(p)
		if e != nil {
			return nil, e
		}
		rel, e := filepath.Rel(m.cfg.UploadDir, abs)
		if e != nil || !safeRelative(rel) {
			return nil, engineFailure(FailureFileReferences, "database references file outside upload root")
		}
		s, e := os.Lstat(abs)
		if e != nil || !s.Mode().IsRegular() {
			return nil, engineFailure(FailureFileReferences, "database references missing or unsafe file")
		}
		out = append(out, pathMapping{p, rel})
		metadataBytes += len(p) + len(rel) + 64
		if metadataBytes > maxManifest/2 {
			return nil, engineFailure(FailureResourceLimit, "file reference metadata exceeds resource limit")
		}
		if len(out) > maxEntries {
			return nil, engineFailure(FailureResourceLimit, "too many file references")
		}
	}
	return out, rows.Err()
}
func (m *Manager) protect(ctx context.Context, paths []pathMapping) error {
	c, e := m.connect(ctx)
	if e != nil {
		return e
	}
	defer c.Close(ctx)
	tx, e := c.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "CREATE TEMP TABLE backup_paths (original text PRIMARY KEY, destination text NOT NULL) ON COMMIT DROP"); e != nil {
		return e
	}
	if _, e = tx.CopyFrom(ctx, pgx.Identifier{"backup_paths"}, []string{"original", "destination"}, pgx.CopyFromSlice(len(paths), func(i int) ([]any, error) {
		return []any{paths[i].Original, filepath.Join(m.cfg.UploadDir, paths[i].Relative)}, nil
	})); e != nil {
		return e
	}
	for _, q := range []string{"UPDATE public.files f SET file_path=p.destination FROM backup_paths p WHERE f.file_path=p.original", "UPDATE public.company_settings s SET invoice_logo_path=p.destination FROM backup_paths p WHERE s.invoice_logo_path=p.original"} {
		if _, e = tx.Exec(ctx, q); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, "UPDATE public.sessions SET expires_at=NOW()-INTERVAL '1 second', revoked_at=NOW()"); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func decodeCopyPath(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		i++
		if i == len(s) {
			return "", engineFailure(FailureFileReferences, "invalid COPY path escape")
		}
		switch s[i] {
		case '\\':
			b.WriteByte('\\')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'v':
			b.WriteByte('\v')
		default:
			return "", engineFailure(FailureFileReferences, "unsupported COPY path escape")
		}
	}
	return b.String(), nil
}

func validateReferences(ctx context.Context, dir string, man manifest) error {
	references := map[string]bool{}
	for _, v := range man.Paths {
		if references[v.Original] {
			return engineFailure(FailureFileReferences, "duplicate path mapping")
		}
		references[v.Original] = true
	}
	return importData(ctx, nil, filepath.Join(dir, "data.sql"), references)
}

// Trusted schema is captured independently from the upload while ordinary
// application admissions prevent a concurrent restore changing the database.
func (m *Manager) localSchema(ctx context.Context, dir string) (string, error) {
	ctx, release, e := m.cfg.Control.Enter(ctx)
	if e != nil {
		return "", e
	}
	defer release()
	if e = privateDir(dir); e != nil {
		return "", e
	}
	f, e := os.OpenFile(filepath.Join(dir, "database.dump"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return "", e
	}
	e = m.command(ctx, "pg_dump", f, "--format=custom", "--schema-only", "--schema=public", "--no-owner", "--no-acl")
	e = errors.Join(e, f.Close())
	if e != nil {
		return "", e
	}
	for _, s := range []string{"pre-data", "post-data"} {
		if e = m.render(ctx, filepath.Join(dir, "database.dump"), s, filepath.Join(dir, s+".sql")); e != nil {
			return "", e
		}
	}
	return schemaHash(filepath.Join(dir, "pre-data.sql"), filepath.Join(dir, "post-data.sql"))
}

func dataStructure(p string) (map[string]string, error) {
	f, e := os.Open(p)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 65536)
	out := map[string]string{}
	for {
		line, e := readLine(r)
		if e == io.EOF {
			return out, nil
		}
		if e != nil {
			return nil, e
		}
		text := strings.TrimSuffix(line, "\n")
		if match := copyPattern.FindStringSubmatch(text); match != nil {
			out["table:"+strings.Trim(match[1], `"`)] = strings.ReplaceAll(match[2], `"`, "")
			if _, e = io.Copy(io.Discard, &copyReader{r: r, pathColumn: -1}); e != nil {
				return nil, e
			}
		} else if match := sequencePattern.FindStringSubmatch(text); match != nil {
			out["sequence:"+match[1]] = ""
		}
	}
}
func (m *Manager) validateStructure(ctx context.Context, dir string) error {
	got, e := dataStructure(filepath.Join(dir, "data.sql"))
	if e != nil {
		return e
	}
	c, e := m.connect(ctx)
	if e != nil {
		return e
	}
	defer c.Close(ctx)
	rows, e := c.Query(ctx, `SELECT 'table:'||c.relname, string_agg(a.attname, ', ' ORDER BY a.attnum) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped AND a.attgenerated='' WHERE n.nspname='public' AND c.relkind='r' GROUP BY c.oid,c.relname UNION ALL SELECT 'sequence:'||c.relname, '' FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind='S'`)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if e = rows.Scan(&key, &value); e != nil {
			return e
		}
		v, ok := got[key]
		if !ok || v != value {
			return engineFailure(FailureSchemaMismatch, "database data inventory differs from trusted schema")
		}
		delete(got, key)
	}
	if e = rows.Err(); e != nil {
		return e
	}
	if len(got) != 0 {
		return engineFailure(FailureSchemaMismatch, "unexpected table or sequence data")
	}
	return nil
}
