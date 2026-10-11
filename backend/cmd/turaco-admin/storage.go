package main

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

type rewrapArgs struct {
	apply bool
	batch int
}

func parseRewrapArgs(args []string) (rewrapArgs, error) {
	a := rewrapArgs{batch: 100}
	fs := newFlagSet("storage rewrap")
	fs.BoolVar(&a.apply, "apply", false, "")
	fs.IntVar(&a.batch, "batch-size", 100, "")
	if err := fs.Parse(args); err != nil {
		return a, fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() != 0 || a.batch < 1 || a.batch > 1000 {
		return a, fmt.Errorf("%w: batch size must be 1 to 1000", errUsage)
	}
	return a, nil
}

func runStorage(ctx context.Context, e env, command string, args []string) error {
	if command != "rewrap" {
		return errUsage
	}
	a, err := parseRewrapArgs(args)
	if err != nil {
		return err
	}
	sc, err := config.LoadStorage()
	if err != nil {
		return err
	}
	vault, err := wiring.StorageVault(e.cfg, sc)
	if err != nil {
		return err
	}
	conn, err := e.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended('turaco-admin:storage-rewrap', 0))`).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return fmt.Errorf("storage rewrap is already running")
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtextextended('turaco-admin:storage-rewrap', 0))`)
	}()
	cursor := "00000000-0000-0000-0000-000000000000"
	var total, pending, changed int
	for {
		rows, err := conn.Query(ctx, `SELECT id::text, object_id FROM platform.attachments
			WHERE id > $1::uuid AND object_purged_at IS NULL ORDER BY id LIMIT $2`, cursor, a.batch)
		if err != nil {
			return err
		}
		type item struct{ id, object string }
		var items []item
		for rows.Next() {
			var it item
			if err := rows.Scan(&it.id, &it.object); err != nil {
				rows.Close()
				return err
			}
			items = append(items, it)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(items) == 0 {
			break
		}
		for _, it := range items {
			cursor = it.id
			total++
			oldID, err := vault.KeyID(ctx, it.object)
			if err != nil {
				return fmt.Errorf("inspect attachment %s: %w", it.id, err)
			}
			if oldID == vault.ActiveKeyID() {
				continue
			}
			pending++
			if !a.apply {
				continue
			}
			// Record intent before touching object storage: a crash between the object write
			// and completion still leaves an immutable audit trail. Retrying is safe.
			correlation := uuid.NewString()
			if err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
				return audit.Record(ctx, tx, audit.Change{Action: "platform.storage.rewrap_started", TargetType: "attachment", TargetID: it.id, Actor: e.auditActor(), CorrelationID: correlation,
					Metadata: map[string]any{"fromKeyId": oldID, "toKeyId": vault.ActiveKeyID()}})
			}); err != nil {
				return err
			}
			did, err := vault.Rewrap(ctx, it.object)
			if err != nil {
				return fmt.Errorf("rewrap attachment %s: %w", it.id, err)
			}
			if !did {
				continue
			}
			changed++
			if err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
				return audit.Record(ctx, tx, audit.Change{Action: "platform.storage.rewrapped", TargetType: "attachment", TargetID: it.id, Actor: e.auditActor(), CorrelationID: correlation,
					Metadata: map[string]any{"fromKeyId": oldID, "toKeyId": vault.ActiveKeyID()}})
			}); err != nil {
				return err
			}
		}
		if len(items) < a.batch {
			break
		}
	}
	if a.apply {
		fmt.Fprintf(e.stdout, "scanned %d attachments; rewrapped %d; active key %s\n", total, changed, vault.ActiveKeyID())
	} else {
		fmt.Fprintf(e.stdout, "dry run: scanned %d attachments; %d need rewrap; active key %s\n", total, pending, vault.ActiveKeyID())
	}
	return nil
}
