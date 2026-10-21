package logx

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// RotatingFile is an append-only file that rolls over once it would exceed
// maxBytes: path becomes path.1, path.1 becomes path.2, and so on up to
// backups copies; the oldest is dropped. With maxBytes 1 MiB and 2 backups
// the log never takes more than 3 MiB on disk.
type RotatingFile struct {
	path     string
	maxBytes int64
	backups  int

	mu   sync.Mutex
	f    *os.File
	size int64
}

// OpenRotating opens (or creates) path for appending.
func OpenRotating(path string, maxBytes int64, backups int) (*RotatingFile, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("logx: maxBytes must be positive")
	}
	if backups < 0 {
		backups = 0
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("logx: create log dir: %w", err)
	}
	r := &RotatingFile{path: path, maxBytes: maxBytes, backups: backups}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *RotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("logx: open %s: %w", r.path, err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

// Write appends p, rotating first when it would push the file past maxBytes.
// A single write larger than maxBytes still goes out whole.
func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return 0, os.ErrClosed
	}
	if r.size > 0 && r.size+int64(len(p)) > r.maxBytes {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *RotatingFile) backupPath(i int) string { return fmt.Sprintf("%s.%d", r.path, i) }

func (r *RotatingFile) rotate() error {
	if err := r.f.Close(); err != nil {
		return err
	}
	r.f = nil
	if r.backups == 0 {
		if err := os.Remove(r.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return r.open()
	}
	_ = os.Remove(r.backupPath(r.backups))
	for i := r.backups - 1; i >= 1; i-- {
		if err := os.Rename(r.backupPath(i), r.backupPath(i+1)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Rename(r.path, r.backupPath(1)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return r.open()
}

// Files lists the log files that exist, oldest first, current last.
func (r *RotatingFile) Files() []string {
	var out []string
	for i := r.backups; i >= 1; i-- {
		if _, err := os.Stat(r.backupPath(i)); err == nil {
			out = append(out, r.backupPath(i))
		}
	}
	if _, err := os.Stat(r.path); err == nil {
		out = append(out, r.path)
	}
	return out
}

// Dump copies every file, oldest first, to w.
func (r *RotatingFile) Dump(w io.Writer) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.Files() {
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, f)
		f.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// Close closes the current file.
func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}
