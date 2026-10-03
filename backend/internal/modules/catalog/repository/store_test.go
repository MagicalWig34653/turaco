package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

type fixture struct {
	t    *testing.T
	pool *pgxpool.Pool
	repo *Repository
	pfx  string
	corr string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := dbtest.Pool(t)
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	f := &fixture{t: t, pool: pool, repo: New(pool), pfx: "zc" + hex.EncodeToString(b)}
	f.corr = "catalog-" + f.pfx
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, f.corr)
		_, _ = pool.Exec(ctx, `DELETE FROM catalog.items WHERE key LIKE $1`, f.pfx+"%")
	})
	return f
}

func (f *fixture) caller() application.Caller {
	return application.Caller{Actor: audit.SystemActor("test"), CorrelationID: f.corr}
}

func (f *fixture) insert(suffix string) application.Item {
	f.t.Helper()
	it, err := f.repo.Insert(context.Background(), f.caller(), application.NewItem{
		Key: f.pfx + suffix, Title: "Secret title " + suffix, Definition: []byte(`{"fields":[{"key":"a","type":"text","label":"A","maxLength":10}],"approvals":[],"fulfillment":[],"allowRequestedFor":false}`),
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return it
}

func TestInsertGetRoundTripAndAudit(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	it := f.insert("-a")
	got, err := f.repo.Get(ctx, it.ID)
	if err != nil || got.Key != f.pfx+"-a" || len(got.Definition.Fields) != 1 || got.Definition.Fields[0].Key != "a" || !got.Active || got.Version != 1 {
		t.Fatalf("get = %+v %v", got, err)
	}
	if _, err := f.repo.Insert(ctx, f.caller(), application.NewItem{Key: f.pfx + "-a", Title: "dup", Definition: []byte(`{}`)}); !errors.Is(err, application.ErrConflict) {
		t.Errorf("duplicate key: %v", err)
	}
	if _, err := f.repo.Get(ctx, "garbage"); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("malformed id: %v", err)
	}
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'catalog.item.created'
		AND NOT (after_data::text LIKE '%Secret title%' OR after_data::text LIKE '%"label"%')`, f.corr).Scan(&n); err != nil || n != 1 {
		t.Errorf("created audit events without content = %d %v", n, err)
	}
}

func TestChangeUpdatesVersionAndAudit(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	it := f.insert("-b")
	out, err := f.repo.Change(ctx, f.caller(), it.ID, func(cur application.Item) (application.Change, error) {
		n := cur
		n.Active = false
		n.Definition.AllowRequestedFor = true
		return application.Change{Next: n, Action: "catalog.item.deactivated"}, nil
	})
	if err != nil || out.Active || !out.Definition.AllowRequestedFor || out.Version != 2 {
		t.Fatalf("change = %+v %v", out, err)
	}
	same, err := f.repo.Change(ctx, f.caller(), it.ID, func(cur application.Item) (application.Change, error) {
		return application.Change{NoChange: true, Next: cur}, nil
	})
	if err != nil || same.Version != 2 {
		t.Errorf("no-op: %+v %v", same, err)
	}
}

func TestListFiltersAndPagination(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a, b, c := f.insert("-1"), f.insert("-2"), f.insert("-3")
	if _, err := f.repo.Change(ctx, f.caller(), b.ID, func(cur application.Item) (application.Change, error) {
		n := cur
		n.Active = false
		return application.Change{Next: n, Action: "catalog.item.deactivated"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{a.ID: true, b.ID: true, c.ID: true}
	collect := func(q application.ListQuery) []string {
		var out []string
		cursor := ""
		for page := 0; page < 1000; page++ {
			q.Page = application.Page{Limit: 2, Cursor: cursor}
			res, err := f.repo.List(ctx, q)
			if err != nil || len(res.Items) > 2 {
				t.Fatalf("list: %v (%d items)", err, len(res.Items))
			}
			for _, it := range res.Items {
				if known[it.ID] {
					out = append(out, it.ID)
				}
			}
			if res.NextCursor == "" {
				return out
			}
			cursor = res.NextCursor
		}
		return out
	}
	if got := collect(application.ListQuery{ActiveOnly: true}); len(got) != 2 || got[0] != a.ID || got[1] != c.ID {
		t.Errorf("active only = %v", got)
	}
	if got := collect(application.ListQuery{}); len(got) != 3 {
		t.Errorf("all = %v", got)
	}
	if got := collect(application.ListQuery{Status: "inactive"}); len(got) != 1 || got[0] != b.ID {
		t.Errorf("inactive = %v", got)
	}
	if _, err := f.repo.List(ctx, application.ListQuery{Page: application.Page{Cursor: "%%%"}}); !errors.Is(err, application.ErrInvalidCursor) {
		t.Errorf("cursor: %v", err)
	}
}

func TestDatabaseInvariants(t *testing.T) {
	f := newFixture(t)
	it := f.insert("-c")
	for name, sql := range map[string]string{
		"bad key":      `UPDATE catalog.items SET key = 'Bad Key' WHERE id = $1`,
		"blank title":  `UPDATE catalog.items SET title = ' ' WHERE id = $1`,
		"array def":    `UPDATE catalog.items SET definition = '[]'::jsonb WHERE id = $1`,
		"zero version": `UPDATE catalog.items SET version = 0 WHERE id = $1`,
	} {
		if _, err := f.pool.Exec(context.Background(), sql, it.ID); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
