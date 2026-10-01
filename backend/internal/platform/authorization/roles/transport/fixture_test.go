package transport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/roles"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

// fakeDirectory is an in-memory roles.SubjectDirectory.
type fakeDirectory struct {
	users  map[string]string
	groups map[string]string
}

func (f *fakeDirectory) UserExists(_ context.Context, id string) (bool, error) {
	_, ok := f.users[id]
	return ok, nil
}

func (f *fakeDirectory) DirectoryGroupObserved(_ context.Context, id string) (bool, error) {
	_, ok := f.groups[id]
	return ok, nil
}

func (f *fakeDirectory) DisplayNames(_ context.Context, u, g []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range u {
		if n, ok := f.users[id]; ok {
			out[id] = n
		}
	}
	for _, id := range g {
		if n, ok := f.groups[id]; ok {
			out[id] = n
		}
	}
	return out, nil
}

func (f *fakeDirectory) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		if _, ok := f.users[id]; ok {
			out[id] = true
		}
	}
	return out, nil
}

type fixed struct {
	p  authorization.Principal
	ok bool
}

func (f fixed) Authenticate(*http.Request) (authorization.Principal, bool, error) {
	return f.p, f.ok, nil
}

type fixture struct {
	t    *testing.T
	pool *pgxpool.Pool
	dir  *fakeDirectory
	svc  *roles.Service
	pfx  string // prefix of every key and correlation id the test creates
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := dbtest.Pool(t)
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	f := &fixture{t: t, pool: pool, pfx: "zt-" + hex.EncodeToString(b)}
	f.dir = &fakeDirectory{users: map[string]string{}, groups: map[string]string{}}
	f.svc = roles.NewService(pool, f.dir)
	t.Cleanup(f.cleanup)
	return f
}

func (f *fixture) cleanup() {
	ctx := context.Background()
	for _, q := range []string{
		`DELETE FROM platform.role_assignments WHERE role_id IN (SELECT id FROM platform.roles WHERE key LIKE $1 || '%')`,
		`DELETE FROM platform.roles WHERE key LIKE $1 || '%'`,
		`DELETE FROM platform.audit_events WHERE correlation_id LIKE $1 || '%'`,
	} {
		if _, err := f.pool.Exec(ctx, q, f.pfx); err != nil {
			f.t.Errorf("cleanup: %v", err)
		}
	}
}

func (f *fixture) newID() string {
	var id string
	if err := f.pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) user(name string) string {
	id := f.newID()
	f.dir.users[id] = name
	return id
}

func (f *fixture) group(name string) string {
	id := f.newID()
	f.dir.groups[id] = name
	return id
}

type auditRow struct {
	actor         *string
	action        string
	correlationID string
}

// wantAudit asserts the audit actions recorded for targetID, oldest first.
func (f *fixture) wantAudit(targetID string, actions ...string) []auditRow {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(), `
		SELECT actor_id::text, action, correlation_id FROM platform.audit_events WHERE target_id = $1 ORDER BY id`, targetID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		if err := rows.Scan(&r.actor, &r.action, &r.correlationID); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, r)
	}
	if len(out) != len(actions) {
		f.t.Fatalf("audit rows for %s = %+v, want actions %v", targetID, out, actions)
	}
	for i := range out {
		if out[i].action != actions[i] || out[i].correlationID == "" {
			f.t.Fatalf("audit rows for %s = %+v, want actions %v", targetID, out, actions)
		}
	}
	return out
}
