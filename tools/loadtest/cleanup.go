package main

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ticketPrefix is what every ticket title created by a run starts with.
func ticketPrefix(tag string) string { return "[lt:" + tag + "]" }

// CleanupResult counts what cleanup found or deleted.
type CleanupResult struct {
	Tickets  int64
	Comments int64
	DryRun   bool
}

// Cleanup deletes the tickets (and their comments) whose title starts with the
// run tag, plus tagged comments on other tickets. With an empty tag every
// load-test tag matches. Comments, the reference registry and problem links of
// deleted tickets go with them (ON DELETE CASCADE). Audit events stay: they are
// append-only. Queue counters never move backwards, so ticket numbers of
// deleted tickets stay unused.
func Cleanup(ctx context.Context, pgURL, tag string, dryRun bool) (CleanupResult, error) {
	res := CleanupResult{DryRun: dryRun}
	prefix, marker := "[lt:", "[lt:"
	if tag != "" {
		prefix = ticketPrefix(tag)
		marker = prefix
	}
	conn, err := pgx.Connect(ctx, pgURL)
	if err != nil {
		return res, fmt.Errorf("connect to Postgres: %w", err)
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)

	if dryRun {
		if err := tx.QueryRow(ctx, `SELECT count(*)::bigint FROM servicedesk.tickets WHERE strpos(title, $1) = 1`, prefix).Scan(&res.Tickets); err != nil {
			return res, err
		}
		err = tx.QueryRow(ctx, `SELECT count(*)::bigint FROM servicedesk.ticket_comments WHERE strpos(body, $1) = 1`, marker).Scan(&res.Comments)
		return res, err
	}
	// Comments written by the load test start with the marker; delete them first so that comments on tickets
	// that the run did not create (none by design) would be removed too.
	ct, err := tx.Exec(ctx, `DELETE FROM servicedesk.ticket_comments WHERE strpos(body, $1) = 1`, marker)
	if err != nil {
		return res, fmt.Errorf("delete comments: %w", err)
	}
	res.Comments = ct.RowsAffected()
	tt, err := tx.Exec(ctx, `DELETE FROM servicedesk.tickets WHERE strpos(title, $1) = 1`, prefix)
	if err != nil {
		return res, fmt.Errorf("delete tickets: %w", err)
	}
	res.Tickets = tt.RowsAffected()
	return res, tx.Commit(ctx)
}
