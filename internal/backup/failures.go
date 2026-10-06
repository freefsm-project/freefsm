package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5/pgconn"
)

type FailureCategory string

const (
	FailureArchiveAuthentication FailureCategory = "archive-authentication"
	FailureArchiveIntegrity      FailureCategory = "archive-integrity"
	FailureArchiveFormat         FailureCategory = "archive-format"
	FailureReleaseMismatch       FailureCategory = "release-mismatch"
	FailureSchemaMismatch        FailureCategory = "schema-mismatch"
	FailureFileReferences        FailureCategory = "file-references"
	FailureStorage               FailureCategory = "storage"
	FailureCapacity              FailureCategory = "capacity"
	FailureResourceLimit         FailureCategory = "resource-limit"
	FailureToolUnavailable       FailureCategory = "tool-unavailable"
	FailureToolCompatibility     FailureCategory = "tool-compatibility"
	FailureDatabaseConnection    FailureCategory = "database-connection"
	FailureDatabasePrivileges    FailureCategory = "database-privileges"
	FailureDatabaseExtraSchemas  FailureCategory = "database-extra-schemas"
	FailureDatabaseExtensions    FailureCategory = "database-extensions"
	FailureDatabaseLargeObjects  FailureCategory = "database-large-objects"
	FailureDatabaseApply         FailureCategory = "database-apply"
	FailureRecoveryRequired      FailureCategory = "recovery-required"
	FailureCancelled             FailureCategory = "cancelled"
	FailureInternal              FailureCategory = "internal"
)

// Failure is safe for authenticated operation status and status-only capabilities.
// DiagnosticID is a correlation ID, not an operation/status bearer capability.
type Failure struct {
	Category                     FailureCategory
	Phase, Message, DiagnosticID string
}

// Diagnostic intentionally has no raw error, stderr, query, argument or path
// fields. At most four causes and eight fixed-vocabulary subprocess signals are
// retained. SQLSTATE is validated as five alphanumeric characters.
type Diagnostic struct {
	Failure           Failure
	Causes            []DiagnosticCause
	PersistenceFailed bool
}
type DiagnosticCause struct {
	Category        FailureCategory
	Reason          string
	Tool            string
	ExitCode        int
	SQLState        string
	Signals         []string
	StderrTruncated bool
}

var failureMessages = map[FailureCategory]string{
	FailureArchiveAuthentication: "Archive authentication failed. Check the archive password and upload the original encrypted file again.",
	FailureArchiveIntegrity:      "Archive integrity verification failed. Upload a complete, unmodified backup.",
	FailureArchiveFormat:         "The archive or database data format is invalid or unsupported. Create a fresh backup with this release.",
	FailureReleaseMismatch:       "The archive release identity does not match this instance. Restore only the same exact release and commit.",
	FailureSchemaMismatch:        "The archive schema differs from the trusted destination schema. Verify release and migration state before retrying.",
	FailureFileReferences:        "Files or database file references are missing or unsafe. Repair the source upload-root references and create a new backup.",
	FailureStorage:               "Backup storage could not be read, written or synchronized. Check storage availability and application-user permissions.",
	FailureCapacity:              "Insufficient storage space. Free space on the staging, upload or PostgreSQL volume before retrying.",
	FailureResourceLimit:         "The operation exceeds a documented backup resource limit. Inspect the operator diagnostic and archive inventory.",
	FailureToolUnavailable:       "A required PostgreSQL tool could not run. Install pg_dump and pg_restore in the application service PATH.",
	FailureToolCompatibility:     "PostgreSQL tools are incompatible. Install pg_dump and pg_restore matching the server major version.",
	FailureDatabaseConnection:    "The database connection failed. Check the destination database service, credentials and connection configuration.",
	FailureDatabasePrivileges:    "The application database role lacks required privileges. It must own public and have database CREATE permission; CREATEDB is not required.",
	FailureDatabaseExtraSchemas:  "Backup requires a public-only application database, but additional schemas are present. Ask the deployment operator to inspect them before any cleanup, then retry.",
	FailureDatabaseExtensions:    "Backup does not support database extensions other than plpgsql. Additional extensions are present. Ask the deployment operator to inspect their dependencies before any cleanup, then retry.",
	FailureDatabaseLargeObjects:  "Backup does not support PostgreSQL large objects, but this database contains them. Ask the deployment operator to inspect their use before any cleanup, then retry.",
	FailureDatabaseApply:         "PostgreSQL rejected database data or schema restoration. Inspect the diagnostic SQLSTATE and verify source data constraints.",
	FailureRecoveryRequired:      "Recovery is incomplete. Normal access remains closed; preserve the journal and recovery copy and run startup recovery after correcting the operator diagnostic.",
	FailureCancelled:             "The operation was cancelled or exceeded its time limit. Check recovery status before retrying.",
	FailureInternal:              "The operation failed unexpectedly. Inspect the correlated operator diagnostic before retrying.",
}

type classifiedError struct {
	category FailureCategory
	cause    error
}

func (e *classifiedError) Error() string                   { return failureMessages[e.category] }
func (e *classifiedError) Unwrap() error                   { return e.cause }
func classified(category FailureCategory, err error) error { return &classifiedError{category, err} }

type commandFailure struct{ diagnostic DiagnosticCause }

func (e *commandFailure) Error() string { return failureMessages[e.diagnostic.Category] }

// engineError categorizes a failure at its producer. reason must be a fixed,
// non-sensitive engine literal, never an external error or interpolated data.
// Changing its explanatory prose cannot change the category.
type engineError struct {
	category FailureCategory
	reason   string
}

func (e *engineError) Error() string { return e.reason }
func engineFailure(category FailureCategory, reason string) error {
	return &engineError{category: category, reason: reason}
}

var sqlStatePattern = regexp.MustCompile(`^[0-9A-Z]{5}$`)

func sanitizedCause(err error) DiagnosticCause {
	d := DiagnosticCause{Category: FailureInternal}
	switch e := err.(type) {
	case *engineError:
		return DiagnosticCause{Category: e.category, Reason: e.reason}
	case *commandFailure:
		return e.diagnostic
	case *pgconn.PgError:
		d.Category = FailureDatabaseApply
		if sqlStatePattern.MatchString(e.Code) {
			d.SQLState = e.Code
		}
		switch {
		case e.Code == "42501":
			d.Category = FailureDatabasePrivileges
		case e.Code == "53100":
			d.Category = FailureCapacity
		case strings.HasPrefix(e.Code, "08") || strings.HasPrefix(e.Code, "28"):
			d.Category = FailureDatabaseConnection
		}
		return d
	case *pgconn.ConnectError:
		d.Category = FailureDatabaseConnection
		var pg *pgconn.PgError
		if errors.As(e, &pg) && sqlStatePattern.MatchString(pg.Code) {
			d.SQLState = pg.Code
		}
		return d
	case *net.OpError:
		d.Category = FailureDatabaseConnection
		return d
	case *os.PathError, *os.LinkError:
		d.Category = FailureStorage
	case *exec.Error:
		d.Category = FailureToolUnavailable
	case *json.SyntaxError, *json.UnmarshalTypeError:
		d.Category = FailureArchiveFormat
	}
	if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) {
		d.Category = FailureCapacity
		return d
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		d.Category = FailureCancelled
		return d
	}
	if errors.Is(err, ErrRecoveryRequired) {
		d.Category = FailureRecoveryRequired
		return d
	}
	if errors.Is(err, ErrUnsupportedRelease) {
		d.Category = FailureReleaseMismatch
		return d
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		d.Category = FailureArchiveIntegrity
		return d
	}
	return d
}

func buildDiagnostic(err error, phase string) Diagnostic {
	d := Diagnostic{Failure: Failure{Phase: phase}}
	// Do not include operation IDs: those are status bearer capabilities.
	var id [16]byte
	if _, e := rand.Read(id[:]); e == nil {
		d.Failure.DiagnosticID = hex.EncodeToString(id[:])
	}
	var visit func(error, int)
	visit = func(e error, depth int) {
		if e == nil || depth > 16 || len(d.Causes) >= 4 {
			return
		}
		if classified, ok := e.(*classifiedError); ok {
			if d.Failure.Category == "" {
				d.Failure.Category = classified.category
			}
			visit(classified.cause, depth+1)
			return
		}
		if joined, ok := e.(interface{ Unwrap() []error }); ok {
			for _, child := range joined.Unwrap() {
				visit(child, depth+1)
			}
			return
		}
		cause := sanitizedCause(e)
		if cause.Category == FailureInternal {
			if wrapped, ok := e.(interface{ Unwrap() error }); ok {
				visit(wrapped.Unwrap(), depth+1)
				return
			}
		}
		d.Causes = append(d.Causes, cause)
	}
	visit(err, 0)
	if d.Failure.Category == "" {
		d.Failure.Category = FailureInternal
		for _, cause := range d.Causes {
			if cause.Category != FailureInternal {
				d.Failure.Category = cause.Category
				break
			}
		}
	}
	if d.Failure.Category == FailureInternal && phase == "validating" {
		d.Failure.Category = FailureArchiveFormat
	}
	d.Failure.Message = failureMessages[d.Failure.Category]
	return d
}

// reportedFailure carries a restore's already-reported work failure through
// rollback to the async job owner. A later publication/recovery failure wraps
// it and receives its own diagnostic rather than replacing the original facts.
type reportedFailure struct {
	error
	failure Failure
}

func (e *reportedFailure) Unwrap() error { return e.error }

func (m *Manager) reportFailure(err error, phase string) Failure {
	// Only the exact error is reusable. Do not use errors.As: a new outer
	// recovery failure must be reported even when its cause was already reported.
	if reported, ok := err.(*reportedFailure); ok {
		return reported.failure
	}
	d := buildDiagnostic(err, phase)
	// One fixed-size record, atomically replaced. Recovery evidence never expires
	// because a diagnostic was written. Reporting failure cannot reopen admission.
	if e := atomicJSON(filepath.Join(m.root, "last-failure.json"), d); e != nil {
		d.PersistenceFailed = true
	}
	if m.cfg.ReportFailure != nil {
		m.cfg.ReportFailure(d)
	}
	return d.Failure
}

// stderrSignals scans only a bounded prefix and retains fixed-vocabulary signals.
// Partial lines use at most 4096 transient bytes; no raw stderr is persisted or
// returned, and the remaining partial line is discarded when the command exits.
type stderrSignals struct {
	line        []byte
	seen        int
	discardLine bool
	truncated   bool
	signals     []string
}

const stderrBudget = 64 << 10
const stderrLineBudget = 4096

var stderrPrefixes = []string{"pg_dump: error: ", "pg_restore: error: "}
var stderrIndicators = []struct{ prefix, signal string }{
	{"permission denied", "permission-denied"},
	{"must be owner", "permission-denied"},
	{"No space left on device", "disk-full"},
	{"could not write to output file: No space left on device", "disk-full"},
	{"could not read from input file: end of file", "archive-truncated"},
	{"could not open input file", "storage-io"},
	{"could not open output file", "storage-io"},
	{"could not extend file", "storage-io"},
	{"input file is too short", "archive-truncated"},
	{"did not find magic string in file header", "archive-format"},
	{"input file does not appear to be a valid archive", "archive-format"},
	{"input file appears to be a text format dump", "archive-format"},
	{"unsupported version (", "archive-version"},
	{"aborting because of server version mismatch", "server-version"},
	{"connection to server", "connection-failed"},
	{"password authentication failed", "authentication-failed"},
	{"no password supplied", "authentication-failed"},
}

func (s *stderrSignals) Write(p []byte) (int, error) {
	n := len(p)
	for _, b := range p {
		if s.seen >= stderrBudget {
			s.truncated = true
			s.line = nil
			break
		}
		s.seen++
		if b == '\n' {
			if !s.discardLine {
				s.inspect(string(s.line))
			}
			s.line = s.line[:0]
			s.discardLine = false
			continue
		}
		if s.discardLine {
			continue
		}
		if len(s.line) >= stderrLineBudget {
			s.line = s.line[:0]
			s.discardLine = true
			s.truncated = true
			continue
		}
		s.line = append(s.line, b)
	}
	return n, nil
}
func (s *stderrSignals) inspect(line string) {
	for _, prefix := range stderrPrefixes {
		if strings.HasPrefix(line, prefix) {
			line = strings.TrimPrefix(line, prefix)
			for _, queryPrefix := range []string{"could not execute query: ERROR:", "query failed: ERROR:"} {
				if strings.HasPrefix(line, queryPrefix) {
					line = strings.TrimLeft(strings.TrimPrefix(line, queryPrefix), " \t")
					break
				}
			}
			for _, item := range stderrIndicators {
				if strings.HasPrefix(line, item.prefix) {
					for _, v := range s.signals {
						if v == item.signal {
							return
						}
					}
					if len(s.signals) < 8 {
						s.signals = append(s.signals, item.signal)
					}
					return
				}
			}
			return
		}
	}
}
func commandDiagnostic(tool string, err error, stderr *stderrSignals) error {
	if tool != "pg_dump" && tool != "pg_restore" {
		tool = "unknown-tool"
	}
	if !stderr.discardLine {
		stderr.inspect(string(stderr.line))
	}
	stderr.line = nil
	d := DiagnosticCause{Category: FailureDatabaseApply, Tool: tool, ExitCode: -1, Signals: stderr.signals, StderrTruncated: stderr.truncated}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		d.ExitCode = exit.ExitCode()
	} else {
		d.Category = FailureToolUnavailable
	}
	for _, signal := range d.Signals {
		switch signal {
		case "permission-denied":
			d.Category = FailureDatabasePrivileges
		case "disk-full":
			d.Category = FailureCapacity
		case "storage-io":
			d.Category = FailureStorage
		case "archive-truncated":
			d.Category = FailureArchiveIntegrity
		case "archive-format":
			d.Category = FailureArchiveFormat
		case "archive-version", "server-version":
			d.Category = FailureToolCompatibility
		case "connection-failed", "authentication-failed":
			d.Category = FailureDatabaseConnection
		}
		break // first native error is primary; retain all signals as context
	}
	return &commandFailure{d}
}
