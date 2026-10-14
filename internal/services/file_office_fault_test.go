package services

import (
	"bytes"
	"io"
	"os"
)

type officeFaultReader struct {
	io.ReaderAt
	calls, failAt int
	cause         error
}

func (r *officeFaultReader) ReadAt(p []byte, off int64) (int, error) {
	r.calls++
	if r.calls == r.failAt {
		return 0, r.cause
	}
	return r.ReaderAt.ReadAt(p, off)
}

// Test-only bridge lets external integration tests run the real publication
// service with a faulting storage reader, without an exported production hook.
func OfficeReadCountForTest(data []byte) (int, error) {
	r := &officeFaultReader{ReaderAt: bytes.NewReader(data)}
	_, err := legacyOfficeReader(r, int64(len(data)))
	return r.calls, err
}

func OfficeReadFaultForTest(s *FileService, failAt int, cause error) (func() bool, func()) {
	injected := false
	previous := s.validateContent
	s.validateContent = func(path, _ string) (string, error) {
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer f.Close()
		stat, err := f.Stat()
		if err != nil {
			return "", err
		}
		r := &officeFaultReader{ReaderAt: f, failAt: failAt, cause: cause}
		mime, err := legacyOfficeReader(r, stat.Size())
		injected = r.calls >= failAt
		return mime, err
	}
	return func() bool { return injected }, func() { s.validateContent = previous }
}
