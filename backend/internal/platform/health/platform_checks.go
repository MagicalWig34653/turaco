package health

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// WorkerFreshness is how recent a heartbeat must be for the worker to count as running.
const WorkerFreshness = 60 * time.Second

// RegisterPlatformChecks registers the checks that read platform tables only: database, migrations, worker, jobs
// and outbox. version is the application version shown by the system page.
func RegisterPlatformChecks(r *Registry, pool *pgxpool.Pool) error {
	checks := []Check{
		{Key: "database", Category: CategorySystem, Run: func(ctx context.Context) Result {
			var v string
			if err := pool.QueryRow(ctx, `SHOW server_version`).Scan(&v); err != nil {
				return Result{Status: StatusFailing, ErrorCode: "database_unreachable"}
			}
			return Result{Status: StatusOK, Detail: map[string]any{"serverVersion": v}}
		}},
		{Key: "migrations", Category: CategorySystem, Run: func(ctx context.Context) Result {
			var version int64
			var name string
			var at time.Time
			var n int
			err := pool.QueryRow(ctx, `SELECT version, name, applied_at FROM platform.schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&version, &name, &at)
			if err != nil {
				return Result{Status: StatusFailing, ErrorCode: "migrations_unreadable", NextStep: &NextStep{Kind: "docs", DocsPath: "docs/development/local-development.md"}}
			}
			_ = pool.QueryRow(ctx, `SELECT count(*) FROM platform.schema_migrations`).Scan(&n)
			// The binary does not embed the migration files (they are applied by turaco-migrate), so "behind the
			// application" cannot be detected here; the applied version is reported for comparison with the release.
			return Result{Status: StatusOK, LastSuccessAt: &at, Counts: map[string]int{"applied": n}, Detail: map[string]any{"latestVersion": version, "latestName": name}}
		}},
		{Key: "worker", Category: CategorySystem, Run: func(ctx context.Context) Result {
			var seen *time.Time
			var running int
			err := pool.QueryRow(ctx, `SELECT max(last_seen_at), count(*) FILTER (WHERE last_seen_at > now() - make_interval(secs => $1)) FROM platform.worker_heartbeats`,
				WorkerFreshness.Seconds()).Scan(&seen, &running)
			if err != nil {
				return Result{Status: StatusFailing, ErrorCode: "heartbeat_unreadable"}
			}
			if seen == nil {
				return Result{Status: StatusUnknown, ErrorCode: "no_heartbeat", NextStep: &NextStep{Kind: "docs", DocsPath: "docs/development/local-development.md"}}
			}
			if running == 0 {
				return Result{Status: StatusFailing, ErrorCode: "worker_not_running", LastSuccessAt: seen, NextStep: &NextStep{Kind: "docs", DocsPath: "docs/development/local-development.md"}}
			}
			return Result{Status: StatusOK, LastSuccessAt: seen, Counts: map[string]int{"workers": running}}
		}},
		{Key: "jobs", Category: CategorySystem, Run: func(ctx context.Context) Result {
			var pending, failed, processing int
			var oldest *time.Time
			err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'pending' AND available_at <= now()),
					count(*) FILTER (WHERE status = 'failed' AND updated_at > now() - interval '24 hours'),
					count(*) FILTER (WHERE status = 'processing'),
					min(available_at) FILTER (WHERE status = 'pending' AND available_at <= now())
				FROM platform.jobs WHERE status IN ('pending','failed','processing')`).Scan(&pending, &failed, &processing, &oldest)
			if err != nil {
				return Result{Status: StatusFailing, ErrorCode: "jobs_unreadable"}
			}
			res := Result{Status: StatusOK, Counts: map[string]int{"due": pending, "processing": processing, "failed24h": failed}}
			if oldest != nil {
				res.Detail = map[string]any{"oldestDueAgeSeconds": int(time.Since(*oldest).Seconds())}
				if time.Since(*oldest) > 15*time.Minute {
					res.Status, res.ErrorCode = StatusStale, "jobs_backlog"
				}
			}
			if failed > 0 && res.Status == StatusOK {
				res.Status, res.ErrorCode = StatusFailing, "jobs_failed"
			}
			return res
		}},
		{Key: "outbox", Category: CategorySystem, Run: func(ctx context.Context) Result {
			var due, failed int
			var oldest *time.Time
			err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status IN ('pending') AND available_at <= now()),
					count(*) FILTER (WHERE status = 'failed'), min(available_at) FILTER (WHERE status = 'pending' AND available_at <= now())
				FROM platform.outbox_events WHERE status IN ('pending','failed')`).Scan(&due, &failed, &oldest)
			if err != nil {
				return Result{Status: StatusFailing, ErrorCode: "outbox_unreadable"}
			}
			res := Result{Status: StatusOK, Counts: map[string]int{"due": due, "failed": failed}}
			if oldest != nil && time.Since(*oldest) > 10*time.Minute {
				res.Status, res.ErrorCode = StatusStale, "outbox_backlog"
			}
			if failed > 0 && res.Status == StatusOK {
				res.Status, res.ErrorCode = StatusFailing, "outbox_failed"
			}
			return res
		}},
	}
	for _, c := range checks {
		if err := r.Register(c); err != nil {
			return err
		}
	}
	return nil
}

// RunHeartbeat upserts the worker heartbeat every 15 seconds until ctx ends and deletes heartbeats older than a day.
func RunHeartbeat(ctx context.Context, pool *pgxpool.Pool, instanceID, version string, logger *slog.Logger) {
	started := time.Now().UTC()
	beat := func() {
		if _, err := pool.Exec(ctx, `INSERT INTO platform.worker_heartbeats (instance_id, version, started_at, last_seen_at) VALUES ($1, $2, $3, now())
			ON CONFLICT (instance_id) DO UPDATE SET last_seen_at = now(), version = EXCLUDED.version`, instanceID, version, started); err != nil && ctx.Err() == nil {
			logger.Warn("worker heartbeat failed", "error", err)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM platform.worker_heartbeats WHERE last_seen_at < now() - interval '1 day'`)
	}
	beat()
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			beat()
		}
	}
}
