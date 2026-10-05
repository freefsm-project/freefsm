package backup

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"filippo.io/age"
)

const maxBytes int64 = 16 << 30
const maxEntries = 100000
const maxManifest = 32 << 20

type payload struct {
	Name   string
	Size   int64
	SHA256 string
}
type pathMapping struct{ Original, Relative string }
type manifest struct {
	BuildKind                       BuildKind `json:",omitempty"`
	Format                          int
	Source, Version, Commit, Schema string
	Captured                        time.Time
	Payloads                        []payload
	Paths                           []pathMapping
}

func syncDir(p string) error {
	f, e := os.Open(p)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}
func atomicJSON(p string, v any) error {
	f, e := os.CreateTemp(filepath.Dir(p), ".journal-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = json.NewEncoder(f).Encode(v); e == nil {
		e = f.Sync()
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return e
	}
	if e = os.Rename(f.Name(), p); e != nil {
		return e
	}
	return syncDir(filepath.Dir(p))
}
func safeRelative(p string) bool {
	return p != "" && p != "." && !strings.Contains(p, "\\") && !strings.ContainsRune(p, 0) && !filepath.IsAbs(p) && filepath.Clean(p) == p && p != ".." && !strings.HasPrefix(p, "../")
}
func privateDir(p string) error {
	if e := os.MkdirAll(p, 0700); e != nil {
		return e
	}
	return checkDirectoryPath(p)
}

// Validate existing ancestors without creating an upload root before recovery.
func checkDirectoryPath(p string) error {
	for q := p; ; q = filepath.Dir(q) {
		s, e := os.Lstat(q)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return e
		}
		if !s.IsDir() || s.Mode()&os.ModeSymlink != 0 {
			return engineFailure(FailureFileReferences, "directory contains symlink")
		}
		if q == filepath.Dir(q) {
			break
		}
	}
	return nil
}
func space(p string, n int64) error {
	var s syscall.Statfs_t
	if e := syscall.Statfs(p, &s); e != nil {
		return e
	}
	if n < 0 || s.Bavail <= 0 || uint64(n)+(256<<20) > uint64(s.Bavail)*uint64(s.Bsize) {
		return engineFailure(FailureCapacity, "insufficient free backup storage")
	}
	return nil
}

type boundedWriter struct {
	w         io.Writer
	remaining int64
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, engineFailure(FailureResourceLimit, "backup output exceeds resource limit")
	}
	n, e := w.w.Write(p)
	w.remaining -= int64(n)
	return n, e
}

type capacityWriter struct {
	dir        string
	w          io.Writer
	sinceCheck int64
}

func (w *capacityWriter) Write(p []byte) (int, error) {
	if w.sinceCheck <= 0 {
		if e := space(w.dir, 16<<20); e != nil {
			return 0, e
		}
		w.sinceCheck = 16 << 20
	}
	n, e := w.w.Write(p)
	w.sinceCheck -= int64(n)
	return n, e
}
func copyFile(dst, src string) error {
	in, e := os.Open(src)
	if e != nil {
		return e
	}
	defer in.Close()
	out, e := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, e = io.Copy(out, in)
	if e == nil {
		e = out.Sync()
	}
	return errors.Join(e, out.Close())
}
func inventory(root string) ([]payload, error) {
	var out []payload
	var total int64
	var metadataBytes int
	e := filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		s, e := d.Info()
		if e != nil {
			return e
		}
		if !s.Mode().IsRegular() {
			return engineFailure(FailureFileReferences, "uploads must contain only regular files and directories")
		}
		rel, e := filepath.Rel(root, p)
		if e != nil || !safeRelative(rel) {
			return engineFailure(FailureFileReferences, "unsafe file path")
		}
		if len(out) >= maxEntries {
			return engineFailure(FailureResourceLimit, "too many files")
		}
		metadataBytes += len(rel) + 128
		if metadataBytes > maxManifest/2 {
			return engineFailure(FailureResourceLimit, "file inventory metadata exceeds resource limit")
		}
		total += s.Size()
		if total > maxBytes {
			return engineFailure(FailureResourceLimit, "instance exceeds resource limit")
		}
		f, e := os.Open(p)
		if e != nil {
			return e
		}
		h := sha256.New()
		n, e := io.Copy(h, f)
		e = errors.Join(e, f.Close())
		if e != nil {
			return e
		}
		out = append(out, payload{rel, n, hex.EncodeToString(h.Sum(nil))})
		return nil
	})
	return out, e
}
func copyTree(dst, src string) error {
	if e := privateDir(dst); e != nil {
		return e
	}
	items, e := inventory(src)
	if e != nil {
		return e
	}
	for _, v := range items {
		p := filepath.Join(dst, v.Name)
		if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
			return e
		}
		if e := copyFile(p, filepath.Join(src, v.Name)); e != nil {
			return e
		}
		digest, e := digestFile(p)
		if e != nil {
			return e
		}
		if digest != v.SHA256 {
			return engineFailure(FailureArchiveIntegrity, "copied file failed digest verification")
		}
	}
	return filepath.WalkDir(dst, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return syncDir(p)
		}
		return nil
	})
}
func seal(dir, password string, m manifest) error {
	r, e := age.NewScryptRecipient(password)
	if e != nil {
		return e
	}
	f, e := os.OpenFile(filepath.Join(dir, "archive.age"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	w, e := age.Encrypt(f, r)
	if e != nil {
		return e
	}
	t := tar.NewWriter(w)
	b, e := json.Marshal(m)
	if e != nil {
		return e
	}
	if len(b) > maxManifest {
		return engineFailure(FailureResourceLimit, "manifest too large")
	}
	if e = t.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(b))}); e != nil {
		return e
	}
	if _, e = t.Write(b); e != nil {
		return e
	}
	for _, v := range m.Payloads {
		if e = t.WriteHeader(&tar.Header{Name: v.Name, Mode: 0600, Size: v.Size}); e != nil {
			return e
		}
		in, e := os.Open(filepath.Join(dir, v.Name))
		if e != nil {
			return e
		}
		_, e = io.Copy(t, in)
		e = errors.Join(e, in.Close())
		if e != nil {
			return e
		}
	}
	if e = t.Close(); e != nil {
		return e
	}
	if e = w.Close(); e != nil {
		return e
	}
	return f.Sync()
}
func unseal(dir, password string) (manifest, error) {
	var m manifest
	f, e := os.Open(filepath.Join(dir, "archive.age"))
	if e != nil {
		return m, e
	}
	defer f.Close()
	id, e := age.NewScryptIdentity(password)
	if e != nil {
		return m, e
	}
	id.SetMaxWorkFactor(18)
	r, e := age.Decrypt(f, id)
	if e != nil {
		return m, classified(FailureArchiveAuthentication, e)
	}
	limited := &io.LimitedReader{R: r, N: maxBytes + maxManifest + 1}
	t := tar.NewReader(limited)
	h, e := t.Next()
	if e != nil {
		return m, e
	}
	if h.Name != "manifest.json" || h.Typeflag != tar.TypeReg || h.Size > maxManifest {
		return m, engineFailure(FailureArchiveFormat, "invalid manifest")
	}
	dec := json.NewDecoder(t)
	dec.DisallowUnknownFields()
	if e = dec.Decode(&m); e != nil {
		return m, e
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return m, engineFailure(FailureArchiveFormat, "trailing manifest data")
	}
	if m.Format != 1 || len(m.Payloads) > maxEntries || len(m.Paths) > maxEntries {
		return m, engineFailure(FailureArchiveFormat, "unsupported manifest")
	}
	if m.Source == "" || len(m.Source) > 256 || m.Captured.IsZero() || m.Captured.After(time.Now().Add(24*time.Hour)) {
		return m, engineFailure(FailureArchiveFormat, "invalid archive identity")
	}
	kind, valid := buildKind(m.BuildKind, m.Version, m.Commit)
	if !valid {
		return m, engineFailure(FailureArchiveFormat, "invalid archive build identity")
	}
	m.BuildKind = kind
	seen := map[string]bool{}
	var total int64
	for _, v := range m.Payloads {
		if !safeRelative(v.Name) || (v.Name != "database.dump" && !strings.HasPrefix(v.Name, "uploads/")) || seen[v.Name] || v.Size < 0 || len(v.SHA256) != 64 {
			return m, engineFailure(FailureArchiveFormat, "invalid inventory")
		}
		seen[v.Name] = true
		if v.Size > maxBytes-total {
			return m, engineFailure(FailureResourceLimit, "archive exceeds resource limit")
		}
		total += v.Size
		h, e = t.Next()
		if e != nil {
			return m, e
		}
		if h.Name != v.Name || h.Typeflag != tar.TypeReg || h.Size != v.Size || h.Linkname != "" {
			return m, engineFailure(FailureArchiveFormat, "archive inventory mismatch")
		}
		if e = space(dir, v.Size); e != nil {
			return m, e
		}
		p := filepath.Join(dir, v.Name)
		if e = os.MkdirAll(filepath.Dir(p), 0700); e != nil {
			return m, e
		}
		out, e := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return m, e
		}
		hash := sha256.New()
		n, e := io.Copy(io.MultiWriter(out, hash), t)
		if e == nil {
			e = out.Sync()
		}
		e = errors.Join(e, out.Close())
		if e != nil {
			return m, e
		}
		if n != v.Size || hex.EncodeToString(hash.Sum(nil)) != v.SHA256 {
			return m, engineFailure(FailureArchiveIntegrity, "payload digest mismatch")
		}
	}
	if !seen["database.dump"] {
		return m, engineFailure(FailureArchiveFormat, "missing database")
	}
	if _, e = t.Next(); e != io.EOF {
		return m, engineFailure(FailureArchiveFormat, "unexpected archive entry")
	}
	// tar EOF is not authenticated age EOF. Drain and authenticate the last chunk.
	n, e := io.Copy(io.Discard, limited)
	if e != nil {
		return m, classified(FailureArchiveIntegrity, e)
	}
	if n != 0 || limited.N == 0 {
		return m, engineFailure(FailureArchiveFormat, "trailing archive content")
	}
	for _, v := range m.Paths {
		if !safeRelative(v.Relative) || !seen["uploads/"+v.Relative] || v.Original == "" {
			return m, engineFailure(FailureFileReferences, "invalid file mapping")
		}
	}
	return m, privateDir(filepath.Join(dir, "uploads"))
}
func digestFile(p string) (string, error) {
	f, e := os.Open(p)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	_, e = io.Copy(h, f)
	return fmt.Sprintf("%x", h.Sum(nil)), e
}
