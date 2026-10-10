package fsstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/storage"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	base := filepath.Join(t.TempDir(), "files")
	s, err := New(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, base
}

func TestPutOpenDelete(t *testing.T) {
	s, base := newStore(t)
	ctx := context.Background()
	id, _ := storage.NewID()
	if err := s.Put(ctx, id, strings.NewReader("hello")); err != nil {
		t.Fatal(err)
	}
	rc, err := s.Open(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "hello" {
		t.Fatalf("got %q", b)
	}
	fi, err := os.Stat(filepath.Join(base, id[:2], id[2:4], id))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode: %v %v", fi, err)
	}
	for _, d := range []string{base, filepath.Join(base, id[:2]), filepath.Join(base, id[:2], id[2:4]), filepath.Join(base, ".tmp")} {
		if fi, err := os.Stat(d); err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("dir %s mode: %v %v", d, fi, err)
		}
	}
	if err := s.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, id); err != nil {
		t.Fatalf("delete is idempotent: %v", err)
	}
	if _, err := s.Open(ctx, id); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("open after delete: %v", err)
	}
}

func TestTraversalAttemptsAreRejected(t *testing.T) {
	s, base := newStore(t)
	outside := filepath.Join(filepath.Dir(base), "outside")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, id := range []string{"../outside", "..", ".", "/etc/passwd", "../../outside", "a/b", `..\outside`, "", strings.Repeat(".", 32), "%2e%2e%2foutside", strings.Repeat("a", 31) + "/"} {
		if err := s.Put(ctx, id, strings.NewReader("x")); !errors.Is(err, storage.ErrInvalidID) {
			t.Errorf("put %q: %v", id, err)
		}
		if _, err := s.Open(ctx, id); !errors.Is(err, storage.ErrInvalidID) {
			t.Errorf("open %q: %v", id, err)
		}
		if err := s.Delete(ctx, id); !errors.Is(err, storage.ErrInvalidID) {
			t.Errorf("delete %q: %v", id, err)
		}
	}
	if b, _ := os.ReadFile(outside); string(b) != "secret" {
		t.Fatal("file outside the base directory was touched")
	}
}

func TestSymlinkEscapeIsBlockedByRoot(t *testing.T) {
	s, base := newStore(t)
	outside := t.TempDir()
	id, _ := storage.NewID()
	// An attacker with write access to the base directory plants a symlink shard pointing outside.
	if err := os.Symlink(outside, filepath.Join(base, id[:2])); err != nil {
		t.Skip("symlinks unavailable")
	}
	if err := s.Put(context.Background(), id, strings.NewReader("x")); err == nil {
		t.Fatal("write through a symlink leaving the base directory succeeded")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatalf("files escaped the base directory: %v", entries)
	}
}

func TestFailedWriteLeavesNoObjectAndNoTempFile(t *testing.T) {
	s, base := newStore(t)
	id, _ := storage.NewID()
	boom := errors.New("boom")
	err := s.Put(context.Background(), id, io.MultiReader(bytes.NewReader([]byte("partial")), errReader{boom}))
	if !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	if _, err := s.Open(context.Background(), id); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("partial object visible: %v", err)
	}
	if e, _ := os.ReadDir(filepath.Join(base, ".tmp")); len(e) != 0 {
		t.Fatalf("temp files left: %v", e)
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func TestCancelledContextAbortsWrite(t *testing.T) {
	s, _ := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	id, _ := storage.NewID()
	if err := s.Put(ctx, id, strings.NewReader("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestNewRequiresAbsolutePathAndCheck(t *testing.T) {
	if _, err := New("relative/dir"); err == nil {
		t.Fatal("relative path accepted")
	}
	if _, err := New(""); err == nil {
		t.Fatal("empty path accepted")
	}
	s, _ := newStore(t)
	if err := s.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestOverwriteReplacesAtomically(t *testing.T) {
	s, _ := newStore(t)
	id, _ := storage.NewID()
	ctx := context.Background()
	_ = s.Put(ctx, id, strings.NewReader("one"))
	_ = s.Put(ctx, id, strings.NewReader("two"))
	rc, _ := s.Open(ctx, id)
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "two" {
		t.Fatalf("got %q", b)
	}
}
