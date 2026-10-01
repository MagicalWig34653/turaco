package audit

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

func TestRecordActorForms(t *testing.T) {
	pool := dbtest.Pool(t)
	ctx := context.Background()
	corr := "record-test-" + t.Name()
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, corr) })
	user := "0190a000-0000-7000-8000-0000000000aa"
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := Record(ctx, tx, Change{Action: "test.user", TargetType: "t", TargetID: "1", Actor: UserActor(user), CorrelationID: corr, After: map[string]string{"k": "v"}}); err != nil {
			return err
		}
		return Record(ctx, tx, Change{Action: "test.cli", TargetType: "t", TargetID: "1", Actor: CLIActor("ops"), CorrelationID: corr, Metadata: map[string]any{"x": 1}})
	})
	if err != nil {
		t.Fatal(err)
	}
	var actorID *string
	var actor, osUser, x string
	if err := pool.QueryRow(ctx, `SELECT actor_id::text, coalesce(metadata->>'actor',''), coalesce(metadata->>'osUser',''), coalesce(metadata->>'x','')
		FROM platform.audit_events WHERE correlation_id = $1 AND action = 'test.cli'`, corr).Scan(&actorID, &actor, &osUser, &x); err != nil {
		t.Fatal(err)
	}
	if actorID != nil || actor != "cli" || osUser != "ops" || x != "1" {
		t.Fatalf("cli row = %v %q %q %q", actorID, actor, osUser, x)
	}
	if err := pool.QueryRow(ctx, `SELECT actor_id::text FROM platform.audit_events WHERE correlation_id = $1 AND action = 'test.user'`, corr).Scan(&actorID); err != nil || actorID == nil || *actorID != user {
		t.Fatalf("user row actor = %v, %v", actorID, err)
	}
}

func TestRecordValidation(t *testing.T) {
	for name, e := range map[string]Change{
		"no actor":   {Action: "a", TargetType: "t", TargetID: "1", CorrelationID: "c"},
		"two actors": {Action: "a", TargetType: "t", TargetID: "1", CorrelationID: "c", Actor: Actor{UserID: "u", System: "cli"}},
		"no action":  {TargetType: "t", TargetID: "1", CorrelationID: "c", Actor: SystemActor("x")},
	} {
		if err := Record(context.Background(), nil, e); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
