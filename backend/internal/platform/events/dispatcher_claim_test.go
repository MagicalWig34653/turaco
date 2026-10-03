package events

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

// The production dispatcher has no event type filter. A nil filter used to
// reach PostgreSQL as NULL and match nothing, so no event was ever claimed.
// The claim runs in a transaction that is rolled back, so this test never
// consumes events other tests have inserted.
func TestUnfilteredDispatcherClaimsDueEvents(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	var id string
	if err := pool.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := func() error {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := InsertOutbox(ctx, tx, OutboxEvent{
			ID: id, EventType: "GoodsReceived", EventVersion: 1, OccurredAt: time.Now(),
			CorrelationID: "corr-unfiltered", Payload: []byte(`{}`),
		}); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE id = $1`, id) })

	for name, opts := range map[string]DispatcherOptions{
		"nil filter":   {},
		"empty filter": {EventTypes: []string{}},
	} {
		d := NewDispatcher(pool, opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = d.claim(ctx, tx)
		_ = tx.Rollback(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("%s: a due event exists but the dispatcher claimed nothing", name)
		} else if err != nil {
			t.Errorf("%s: claim: %v", name, err)
		}
	}
}
