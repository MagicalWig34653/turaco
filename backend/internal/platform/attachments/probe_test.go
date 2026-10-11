package attachments_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/attachments"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/storage"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/storage/fsstore"
)

func TestProbeStorageReadsWithoutWriting(t *testing.T) {
	e := newEnv(t, 1<<20)
	ctx := context.Background()
	a, err := e.upload(t, e.author, "", pdf)
	if err != nil {
		t.Fatal(err)
	}
	before := e.objectFiles()
	if err := e.svc.ProbeStorage(ctx); err != nil {
		t.Fatalf("probe with a stored object: %v", err)
	}
	if after := e.objectFiles(); len(after) != len(before) {
		t.Errorf("the probe must not write: %d -> %d objects", len(before), len(after))
	}
	// A stored object that cannot be read any more is a failing probe, not an "ok because not found".
	var obj string
	if err := e.pool.QueryRow(ctx, `SELECT object_id FROM platform.attachments WHERE id = $1::uuid`, a.ID).Scan(&obj); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(e.base, obj[:2], obj[2:4], obj)); err != nil {
		t.Fatal(err)
	}
	err = e.svc.ProbeStorage(ctx)
	if err == nil || !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("missing stored object: %v", err)
	}
}

type pingScanner struct {
	fakeScanner
	pingErr error
}

func (p *pingScanner) Ping(context.Context) error { return p.pingErr }

func TestPingScannerUsesTheScannersPing(t *testing.T) {
	e := newEnv(t, 1<<20)
	fs, err := fsstore.New(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	vault, err := storage.NewVault(fs, bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := attachments.NewPolicy(1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ps := &pingScanner{pingErr: attachments.ErrScannerUnavailable}
	svc := attachments.New(e.pool, vault, ps, policy, logger)
	if err := svc.PingScanner(context.Background()); !errors.Is(err, attachments.ErrScannerUnavailable) {
		t.Errorf("unreachable scanner: %v", err)
	}
	ps.pingErr = nil
	if err := svc.PingScanner(context.Background()); err != nil {
		t.Errorf("reachable scanner: %v", err)
	}
	// A scanner without a ping (the fake) is not a failing probe.
	if err := e.svc.PingScanner(context.Background()); err != nil {
		t.Errorf("scanner without ping: %v", err)
	}
}
