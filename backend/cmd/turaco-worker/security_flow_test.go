package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	securityapp "github.com/MagicalWig34653/turaco/backend/internal/modules/security/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

// An advisory created by the API service queues a match job; the worker registration executes that
// job and records the revision it matched, even when there are no matching installations yet.
func TestSecurityAdvisoryMatchingJobFlow(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	svc := wiring.Security(w.pool)
	advisory, _, err := svc.Create(ctx, securityapp.Caller{Actor: audit.UserActor(w.creator), CorrelationID: w.corr},
		securityapp.Principal{UserID: w.creator, Manage: true},
		securityapp.AdvisoryInput{Title: w.corr + " advisory", Severity: "high"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		conn, err := w.pool.Acquire(ctx)
		if err != nil {
			return
		}
		defer conn.Release()
		_, _ = conn.Exec(ctx, `SET session_replication_role = replica`)
		_, _ = conn.Exec(ctx, `DELETE FROM security.finding_transitions WHERE finding_id IN (SELECT id FROM security.vulnerability_findings WHERE advisory_id = $1::uuid)`, advisory.ID)
		_, _ = conn.Exec(ctx, `DELETE FROM security.vulnerability_findings WHERE advisory_id = $1::uuid`, advisory.ID)
		_, _ = conn.Exec(ctx, `DELETE FROM security.advisory_transitions WHERE advisory_id = $1::uuid`, advisory.ID)
		_, _ = conn.Exec(ctx, `DELETE FROM security.advisory_criteria_rules WHERE criteria_id IN (SELECT id FROM security.advisory_criteria WHERE advisory_id = $1::uuid)`, advisory.ID)
		_, _ = conn.Exec(ctx, `DELETE FROM security.advisory_criteria WHERE advisory_id = $1::uuid`, advisory.ID)
		_, _ = conn.Exec(ctx, `DELETE FROM security.advisories WHERE id = $1::uuid`, advisory.ID)
		_, _ = conn.Exec(ctx, `RESET session_replication_role`)
		_, _ = w.pool.Exec(ctx, `DELETE FROM platform.jobs WHERE dedupe_key = $1`, securityapp.MatchJobType+":"+advisory.ID)
	})

	runner := jobs.NewRunner(w.pool, jobs.RunnerOptions{WorkerID: "security-flow-" + w.corr}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := registerSecurityMatching(runner, w.pool); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := runner.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		var revision *int
		if err := w.pool.QueryRow(ctx, `SELECT matched_revision FROM security.advisories WHERE id = $1::uuid`, advisory.ID).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		if revision != nil && *revision == advisory.CriteriaRevision {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("security.match did not record the advisory's criteria revision")
}
