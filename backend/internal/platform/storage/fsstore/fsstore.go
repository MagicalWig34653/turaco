// Package fsstore is the filesystem adapter of the storage port (ADR-0037). All file system access goes through an
// os.Root opened on the base directory, so no id, link or relative path can leave it; ids are validated first and
// become two shard directories plus the file name. Directories are 0700, files 0600.
package fsstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/storage"
)

const (
	tmpDir  = ".tmp"
	dirMode = 0o700
	fileMod = 0o600
)

// Store is the filesystem Backend.
type Store struct {
	root *os.Root
}

// New opens (and creates, mode 0700) the base directory. The path must be absolute.
func New(base string) (*Store, error) {
	if base == "" || !filepath.IsAbs(base) {
		return nil, errors.New("fsstore: STORAGE_PATH must be an absolute path")
	}
	if err := os.MkdirAll(base, dirMode); err != nil {
		return nil, fmt.Errorf("fsstore: create base directory: %w", err)
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, fmt.Errorf("fsstore: open base directory: %w", err)
	}
	if err := root.MkdirAll(tmpDir, dirMode); err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("fsstore: create temp directory: %w", err)
	}
	return &Store{root: root}, nil
}

// Close releases the base directory handle.
func (s *Store) Close() error { return s.root.Close() }

// Driver implements storage.Backend.
func (s *Store) Driver() string { return "filesystem" }

func rel(id string) string { return path.Join(id[:2], id[2:4], id) }

// Put implements storage.Backend: temp file, fsync, rename, directory fsync.
func (s *Store) Put(ctx context.Context, id string, r io.Reader) (err error) {
	if !storage.ValidID(id) {
		return storage.ErrInvalidID
	}
	tmpID, err := storage.NewID()
	if err != nil {
		return err
	}
	tmp := path.Join(tmpDir, tmpID)
	f, err := s.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMod)
	if err != nil {
		return fmt.Errorf("fsstore: create temp file: %w", err)
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = s.root.Remove(tmp)
		}
	}()
	if _, err = io.Copy(f, &ctxReader{ctx: ctx, r: r}); err != nil {
		return fmt.Errorf("fsstore: write: %w", err)
	}
	if err = f.Sync(); err != nil {
		return fmt.Errorf("fsstore: sync: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("fsstore: close: %w", err)
	}
	dir := path.Dir(rel(id))
	if err = s.root.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("fsstore: create shard directory: %w", err)
	}
	if err = s.root.Rename(tmp, rel(id)); err != nil {
		return fmt.Errorf("fsstore: rename: %w", err)
	}
	if d, derr := s.root.Open(dir); derr == nil { // persist the rename; a failure here is not an error of the write
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// Open implements storage.Backend.
func (s *Store) Open(_ context.Context, id string) (io.ReadCloser, error) {
	if !storage.ValidID(id) {
		return nil, storage.ErrInvalidID
	}
	f, err := s.root.Open(rel(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, storage.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("fsstore: open: %w", err)
	}
	return f, nil
}

// Delete implements storage.Backend.
func (s *Store) Delete(_ context.Context, id string) error {
	if !storage.ValidID(id) {
		return storage.ErrInvalidID
	}
	if err := s.root.Remove(rel(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("fsstore: delete: %w", err)
	}
	return nil
}

// Check writes, reads and removes a probe file in the temp directory.
func (s *Store) Check(context.Context) error {
	id, err := storage.NewID()
	if err != nil {
		return err
	}
	p := path.Join(tmpDir, "probe-"+id)
	f, err := s.root.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMod)
	if err != nil {
		return fmt.Errorf("fsstore: probe: %w", err)
	}
	_, werr := f.Write([]byte("ok"))
	cerr := f.Close()
	rerr := s.root.Remove(p)
	return errors.Join(werr, cerr, rerr)
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
