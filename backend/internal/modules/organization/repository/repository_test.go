package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
)

const missingID = "00000000-0000-7000-8000-000000000000"

type fixture struct {
	t    *testing.T
	pool *pgxpool.Pool
	repo *Repository
	pfx  string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Skipf("database unavailable: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("database unreachable: %v", err)
	}
	t.Cleanup(pool.Close)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return &fixture{t: t, pool: pool, repo: New(pool), pfx: "zt" + hex.EncodeToString(b)}
}

// insert runs sql, scans the returned id and registers cleanup SQL (run in reverse order).
func (f *fixture) insert(sql string, cleanup string, args ...any) string {
	f.t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		f.t.Fatalf("insert: %v", err)
	}
	f.t.Cleanup(func() {
		if _, err := f.pool.Exec(context.Background(), cleanup, id); err != nil {
			f.t.Errorf("cleanup: %v", err)
		}
	})
	return id
}

func (f *fixture) user(name, status string) string {
	return f.insert(`INSERT INTO organization.users(display_name, status) VALUES ($1,$2) RETURNING id::text`,
		`DELETE FROM organization.users WHERE id = $1`, name, status)
}

func (f *fixture) team(name string) string {
	return f.insert(`INSERT INTO organization.teams(name) VALUES ($1) RETURNING id::text`,
		`DELETE FROM organization.teams WHERE id = $1`, name)
}

func (f *fixture) location(name string) string {
	return f.insert(`INSERT INTO organization.locations(name) VALUES ($1) RETURNING id::text`,
		`DELETE FROM organization.locations WHERE id = $1`, name)
}

func (f *fixture) group(name string, deleted *time.Time) string {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return f.insert(`INSERT INTO organization.directory_groups(provider_key, external_id, display_name, description, first_observed_at, last_observed_at, deleted_observed_at)
VALUES ('test', $1, $1, 'desc', $2, $2, $3) RETURNING id::text`,
		`DELETE FROM organization.directory_groups WHERE id = $1`, name, now, deleted)
}

func (f *fixture) exec(sql string, cleanup string, args ...any) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatalf("exec: %v", err)
	}
	if cleanup != "" {
		f.t.Cleanup(func() {
			if _, err := f.pool.Exec(context.Background(), cleanup, args[0]); err != nil {
				f.t.Errorf("cleanup: %v", err)
			}
		})
	}
}

func TestListUsersPaginationAndFilters(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	want := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		status := "active"
		if i == 4 {
			status = "inactive"
		}
		want = append(want, f.user(f.pfx+"-user-"+string(rune('a'+i)), status))
	}
	f.user("other-"+f.pfx, "active")

	var got []string
	cursor := ""
	pages := 0
	for {
		res, err := f.repo.ListUsers(ctx, application.UserFilter{Query: f.pfx, Page: application.Page{Limit: 2, Cursor: cursor}})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, u := range res.Items {
			got = append(got, u.ID)
		}
		if res.NextCursor == "" {
			break
		}
		if res.NextCursor != res.Items[len(res.Items)-1].ID {
			t.Fatalf("next cursor %q is not last item id", res.NextCursor)
		}
		cursor = res.NextCursor
	}
	if pages != 3 || len(got) != 5 {
		t.Fatalf("pages=%d items=%d", pages, len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order mismatch at %d", i)
		}
	}

	// Limit boundaries: exactly N items => no cursor; N-1 => cursor.
	res, err := f.repo.ListUsers(ctx, application.UserFilter{Query: f.pfx, Page: application.Page{Limit: 5}})
	if err != nil || len(res.Items) != 5 || res.NextCursor != "" {
		t.Fatalf("limit=5: %v items=%d cursor=%q", err, len(res.Items), res.NextCursor)
	}
	res, err = f.repo.ListUsers(ctx, application.UserFilter{Query: f.pfx, Page: application.Page{Limit: 4}})
	if err != nil || len(res.Items) != 4 || res.NextCursor == "" {
		t.Fatalf("limit=4: %v items=%d cursor=%q", err, len(res.Items), res.NextCursor)
	}
	res, err = f.repo.ListUsers(ctx, application.UserFilter{Query: f.pfx, Page: application.Page{Limit: 1}})
	if err != nil || len(res.Items) != 1 || res.NextCursor == "" {
		t.Fatalf("limit=1: %v", err)
	}

	// Case-insensitive prefix and status filter.
	res, err = f.repo.ListUsers(ctx, application.UserFilter{Query: upper(f.pfx), Status: "inactive"})
	if err != nil || len(res.Items) != 1 || res.Items[0].ID != want[4] || res.Items[0].Status != "inactive" {
		t.Fatalf("status filter: %v %+v", err, res.Items)
	}
	// Prefix only: "user" in the middle must not match.
	res, err = f.repo.ListUsers(ctx, application.UserFilter{Query: "user-" + f.pfx})
	if err != nil || len(res.Items) != 0 {
		t.Fatalf("non-prefix matched: %v %d", err, len(res.Items))
	}
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

func TestListUsersLikeMetacharactersAreLiteral(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	pct := f.user(f.pfx+"%x", "active")
	f.user(f.pfx+"yx", "active")
	us := f.user(f.pfx+"_x", "active")
	f.user(f.pfx+"zx", "active")
	bs := f.user(f.pfx+`\x`, "active")

	for _, tc := range []struct{ q, id string }{
		{f.pfx + "%", pct},
		{f.pfx + "_", us},
		{f.pfx + `\`, bs},
	} {
		res, err := f.repo.ListUsers(ctx, application.UserFilter{Query: tc.q})
		if err != nil || len(res.Items) != 1 || res.Items[0].ID != tc.id {
			t.Fatalf("query %q: %v %+v", tc.q, err, res.Items)
		}
	}
}

func TestUserGetAndNullableFields(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := f.user(f.pfx+"-get", "active")
	u, err := f.repo.GetUser(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != id || u.DisplayName != f.pfx+"-get" || u.GivenName != nil || u.PrimaryEmail != nil ||
		u.DepartmentID != nil || u.ManagerUserID != nil || u.UpdatedAt.IsZero() {
		t.Fatalf("unexpected user %+v", u)
	}
	for _, bad := range []string{missingID, "not-a-uuid", ""} {
		if _, err := f.repo.GetUser(ctx, bad); !errors.Is(err, application.ErrNotFound) {
			t.Fatalf("GetUser(%q) = %v", bad, err)
		}
	}
}

func TestInvalidCursor(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	bad := application.Page{Cursor: "nope"}
	if _, err := f.repo.ListUsers(ctx, application.UserFilter{Page: bad}); !errors.Is(err, application.ErrInvalidCursor) {
		t.Fatalf("users: %v", err)
	}
	if _, err := f.repo.ListTeams(ctx, application.NameFilter{Page: bad}); !errors.Is(err, application.ErrInvalidCursor) {
		t.Fatalf("teams: %v", err)
	}
	if _, err := f.repo.ListLocations(ctx, application.NameFilter{Page: bad}); !errors.Is(err, application.ErrInvalidCursor) {
		t.Fatalf("locations: %v", err)
	}
	if _, err := f.repo.ListDirectoryGroups(ctx, application.NameFilter{Page: bad}); !errors.Is(err, application.ErrInvalidCursor) {
		t.Fatalf("groups: %v", err)
	}
	team := f.team(f.pfx + "-t")
	if _, err := f.repo.ListTeamMembers(ctx, team, bad); !errors.Is(err, application.ErrInvalidCursor) {
		t.Fatalf("team members: %v", err)
	}
	group := f.group(f.pfx+"-g", nil)
	if _, err := f.repo.ListDirectoryGroupMembers(ctx, group, bad); !errors.Is(err, application.ErrInvalidCursor) {
		t.Fatalf("group members: %v", err)
	}
}

func TestNotFound(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for name, err := range map[string]error{
		"team":     func() error { _, e := f.repo.GetTeam(ctx, missingID); return e }(),
		"location": func() error { _, e := f.repo.GetLocation(ctx, missingID); return e }(),
		"group":    func() error { _, e := f.repo.GetDirectoryGroup(ctx, "x"); return e }(),
		"members":  func() error { _, e := f.repo.ListTeamMembers(ctx, missingID, application.Page{}); return e }(),
		"gmembers": func() error { _, e := f.repo.ListDirectoryGroupMembers(ctx, "x", application.Page{}); return e }(),
	} {
		if !errors.Is(err, application.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestTeamsAndLocations(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	t1 := f.team(f.pfx + "-a")
	f.team(f.pfx + "-b")
	f.team(f.pfx + "-c")
	l1 := f.location(f.pfx + "-a")
	f.location(f.pfx + "-b")

	res, err := f.repo.ListTeams(ctx, application.NameFilter{Query: f.pfx, Page: application.Page{Limit: 2}})
	if err != nil || len(res.Items) != 2 || res.NextCursor == "" || res.Items[0].ID != t1 {
		t.Fatalf("teams page 1: %v %+v", err, res)
	}
	res2, err := f.repo.ListTeams(ctx, application.NameFilter{Query: f.pfx, Page: application.Page{Limit: 2, Cursor: res.NextCursor}})
	if err != nil || len(res2.Items) != 1 || res2.NextCursor != "" {
		t.Fatalf("teams page 2: %v %+v", err, res2)
	}
	tm, err := f.repo.GetTeam(ctx, t1)
	if err != nil || tm.Name != f.pfx+"-a" || !tm.Active {
		t.Fatalf("GetTeam: %v %+v", err, tm)
	}

	lres, err := f.repo.ListLocations(ctx, application.NameFilter{Query: f.pfx})
	if err != nil || len(lres.Items) != 2 || lres.Items[0].ID != l1 || lres.Items[0].ExternalKey != nil {
		t.Fatalf("locations: %v %+v", err, lres)
	}
	loc, err := f.repo.GetLocation(ctx, l1)
	if err != nil || loc.Name != f.pfx+"-a" {
		t.Fatalf("GetLocation: %v %+v", err, loc)
	}
}

func TestListTeamMembersOnlyCurrent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	team := f.team(f.pfx + "-team")
	cur := f.user(f.pfx+"-current", "active")
	expired := f.user(f.pfx+"-expired", "active")
	future := f.user(f.pfx+"-future", "active")
	multi := f.user(f.pfx+"-multi", "active")

	f.exec(`INSERT INTO organization.team_memberships(team_id,user_id,role,valid_from,source) VALUES ($1,$2,'lead',now() - interval '1 day','platform')`,
		`DELETE FROM organization.team_memberships WHERE team_id = $1`, team, cur)
	f.exec(`INSERT INTO organization.team_memberships(team_id,user_id,valid_from,valid_until) VALUES ($1,$2,now() - interval '2 day', now() - interval '1 day')`,
		"", team, expired)
	f.exec(`INSERT INTO organization.team_memberships(team_id,user_id,valid_from) VALUES ($1,$2,now() + interval '1 day')`,
		"", team, future)
	f.exec(`INSERT INTO organization.team_memberships(team_id,user_id,role,valid_from) VALUES ($1,$2,'old',now() - interval '3 day'), ($1,$2,'new',now() - interval '1 day')`,
		"", team, multi)

	res, err := f.repo.ListTeamMembers(ctx, team, application.Page{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 2 || res.NextCursor != "" {
		t.Fatalf("items=%+v", res.Items)
	}
	byID := map[string]application.TeamMember{}
	for _, m := range res.Items {
		byID[m.UserID] = m
	}
	if m, ok := byID[cur]; !ok || m.Role == nil || *m.Role != "lead" || m.Source != "platform" || m.DisplayName != f.pfx+"-current" {
		t.Fatalf("current member: %+v", m)
	}
	if m, ok := byID[multi]; !ok || m.Role == nil || *m.Role != "new" {
		t.Fatalf("multi member: %+v", m)
	}

	// Pagination by user id.
	p1, err := f.repo.ListTeamMembers(ctx, team, application.Page{Limit: 1})
	if err != nil || len(p1.Items) != 1 || p1.NextCursor != p1.Items[0].UserID {
		t.Fatalf("page1: %v %+v", err, p1)
	}
	p2, err := f.repo.ListTeamMembers(ctx, team, application.Page{Limit: 1, Cursor: p1.NextCursor})
	if err != nil || len(p2.Items) != 1 || p2.NextCursor != "" || p2.Items[0].UserID == p1.Items[0].UserID {
		t.Fatalf("page2: %v %+v", err, p2)
	}
}

func TestDirectoryGroupsAndMembers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	deletedAt := time.Now().UTC().Truncate(time.Microsecond)
	g1 := f.group(f.pfx+"-live", nil)
	g2 := f.group(f.pfx+"-gone", &deletedAt)
	u1 := f.user(f.pfx+"-u1", "active")
	u2 := f.user(f.pfx+"-u2", "active")
	f.exec(`INSERT INTO organization.directory_group_memberships(group_id,user_id,last_observed_at) VALUES ($1,$2,now()),($1,$3,now())`,
		`DELETE FROM organization.directory_group_memberships WHERE group_id = $1`, g1, u1, u2)

	res, err := f.repo.ListDirectoryGroups(ctx, application.NameFilter{Query: f.pfx})
	if err != nil || len(res.Items) != 2 || res.Items[0].ID != g1 || res.Items[1].ID != g2 {
		t.Fatalf("list: %v %+v", err, res)
	}
	if res.Items[0].DeletedObservedAt != nil || res.Items[1].DeletedObservedAt == nil {
		t.Fatalf("deleted flags: %+v", res.Items)
	}
	g, err := f.repo.GetDirectoryGroup(ctx, g2)
	if err != nil || g.DeletedObservedAt == nil || !g.DeletedObservedAt.Equal(deletedAt) ||
		g.Description == nil || *g.Description != "desc" || g.ProviderKey != "test" {
		t.Fatalf("soft-deleted group: %v %+v", err, g)
	}

	m1, err := f.repo.ListDirectoryGroupMembers(ctx, g1, application.Page{Limit: 1})
	if err != nil || len(m1.Items) != 1 || m1.NextCursor == "" || m1.Items[0].UserID != u1 {
		t.Fatalf("members page1: %v %+v", err, m1)
	}
	m2, err := f.repo.ListDirectoryGroupMembers(ctx, g1, application.Page{Limit: 1, Cursor: m1.NextCursor})
	if err != nil || len(m2.Items) != 1 || m2.NextCursor != "" || m2.Items[0].UserID != u2 ||
		m2.Items[0].DisplayName != f.pfx+"-u2" || m2.Items[0].LastObservedAt.IsZero() {
		t.Fatalf("members page2: %v %+v", err, m2)
	}
	empty, err := f.repo.ListDirectoryGroupMembers(ctx, g2, application.Page{})
	if err != nil || len(empty.Items) != 0 || empty.NextCursor != "" {
		t.Fatalf("empty members: %v %+v", err, empty)
	}
}
