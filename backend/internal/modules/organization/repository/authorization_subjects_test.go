package repository

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
)

func (f *fixture) member(group, user string, ended bool) {
	f.t.Helper()
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	var until *time.Time
	if ended {
		until = &at
	}
	f.exec(`INSERT INTO organization.directory_group_memberships(group_id, user_id, observed_from, last_observed_at, observed_until)
		VALUES ($1, $2, $3, $3, $4)`, `DELETE FROM organization.directory_group_memberships WHERE group_id = $1`,
		group, user, at.Add(-time.Hour), until)
}

// nest records parent <- child (child is a member of parent).
func (f *fixture) nest(parent, child string, ended bool) {
	f.t.Helper()
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	var until *time.Time
	if ended {
		until = &at
	}
	f.exec(`INSERT INTO organization.directory_group_nesting(parent_group_id, child_group_id, observed_from, last_observed_at, observed_until)
		VALUES ($1, $2, $3, $3, $4)`, `DELETE FROM organization.directory_group_nesting WHERE parent_group_id = $1`,
		parent, child, at.Add(-time.Hour), until)
}

func sorted(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

func sameSet(t *testing.T, got []string, want ...string) {
	t.Helper()
	g, w := sorted(got), sorted(want)
	if strings.Join(g, ",") != strings.Join(w, ",") {
		t.Fatalf("groups = %v, want %v", g, w)
	}
}

func TestGroupIDsOfUserTransitiveAndCycleSafe(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.user(f.pfx+"-u", "active")
	other := f.user(f.pfx+"-other", "active")
	g1, g2, g3, g4 := f.group(f.pfx+"-g1", nil), f.group(f.pfx+"-g2", nil), f.group(f.pfx+"-g3", nil), f.group(f.pfx+"-g4", nil)
	f.member(g1, u, false)
	f.member(g4, other, false)
	f.nest(g2, g1, false) // g1 is a member of g2
	f.nest(g3, g2, false) // g2 is a member of g3

	got, err := f.repo.GroupIDsOfUser(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	sameSet(t, got, g1, g2, g3)

	// A cycle (g3 is a member of g1) must terminate and add nothing new.
	f.nest(g1, g3, false)
	if got, err = f.repo.GroupIDsOfUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	sameSet(t, got, g1, g2, g3)

	// Nesting only expands upwards: members of the parent do not inherit child groups.
	if got, err = f.repo.GroupIDsOfUser(ctx, other); err != nil {
		t.Fatal(err)
	}
	sameSet(t, got, g4)

	none, err := f.repo.GroupIDsOfUser(ctx, f.user(f.pfx+"-nobody", "active"))
	if err != nil || none == nil || len(none) != 0 {
		t.Fatalf("no groups = %v, %v", none, err)
	}
}

func TestGroupIDsOfUserIgnoresEndedAndDeleted(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.user(f.pfx+"-u", "active")
	deleted := time.Now().UTC()
	gMember, gEndedMember := f.group(f.pfx+"-m", nil), f.group(f.pfx+"-em", nil)
	gParent, gEndedParent, gDeleted, gAboveDeleted := f.group(f.pfx+"-p", nil), f.group(f.pfx+"-ep", nil), f.group(f.pfx+"-d", &deleted), f.group(f.pfx+"-ad", nil)
	f.member(gMember, u, false)
	f.member(gEndedMember, u, true)     // membership no longer observed
	f.nest(gParent, gMember, false)     // counts
	f.nest(gEndedParent, gMember, true) // edge no longer observed
	f.nest(gDeleted, gMember, false)    // deleted parent: neither included nor traversed
	f.nest(gAboveDeleted, gDeleted, false)

	got, err := f.repo.GroupIDsOfUser(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	sameSet(t, got, gMember, gParent)

	// A deleted group the user is directly a member of does not count either.
	f.member(gDeleted, u, false)
	if got, err = f.repo.GroupIDsOfUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	sameSet(t, got, gMember, gParent)
}

func TestAuthorizationSubjectLookups(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.user(f.pfx+"-user", "active")
	g := f.group(f.pfx+"-group", nil)
	deleted := time.Now().UTC()
	gd := f.group(f.pfx+"-gone", &deleted)
	missing := "00000000-0000-7000-8000-000000000001"

	for id, want := range map[string]bool{u: true, missing: false} {
		if got, err := f.repo.UserExists(ctx, id); err != nil || got != want {
			t.Fatalf("UserExists(%s) = %v, %v", id, got, err)
		}
	}
	for id, want := range map[string]bool{g: true, gd: false, u: false, missing: false} {
		if got, err := f.repo.DirectoryGroupObserved(ctx, id); err != nil || got != want {
			t.Fatalf("DirectoryGroupObserved(%s) = %v, %v", id, got, err)
		}
	}
	inactive := f.user(f.pfx+"-inactive", "inactive")
	active, err := f.repo.ActiveUsers(ctx, []string{u, inactive, g, missing})
	if err != nil || len(active) != 1 || !active[u] {
		t.Fatalf("ActiveUsers = %v, %v", active, err)
	}
	if active, err = f.repo.ActiveUsers(ctx, nil); err != nil || len(active) != 0 {
		t.Fatalf("empty ActiveUsers = %v, %v", active, err)
	}
	names, err := f.repo.DisplayNames(ctx, []string{u, missing}, []string{g})
	if err != nil || len(names) != 2 || names[u] != f.pfx+"-user" || names[g] != f.pfx+"-group" {
		t.Fatalf("names = %v, %v", names, err)
	}
	if names, err = f.repo.DisplayNames(ctx, nil, nil); err != nil || len(names) != 0 {
		t.Fatalf("empty names = %v, %v", names, err)
	}
}

func TestFindUser(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	svc := application.NewAuthorizationSubjects(f.repo)
	u := f.user(f.pfx+"-user", "active")
	byEmail := f.user(f.pfx+"-mail", "active")
	f.exec(`UPDATE organization.users SET primary_email = $2 WHERE id = $1`, ``, byEmail, f.pfx+"-Mail@Example.Test")
	other := f.user(f.pfx+"-other", "active")
	f.insert(`INSERT INTO organization.external_identities(user_id, provider_key, external_subject, username) VALUES ($1,'p1',$2,$3) RETURNING id::text`,
		`DELETE FROM organization.external_identities WHERE id = $1`, u, f.pfx+"-s1", f.pfx+"-Alice")
	// The same username under another provider and another user is ambiguous.
	f.insert(`INSERT INTO organization.external_identities(user_id, provider_key, external_subject, username) VALUES ($1,'p2',$2,$3) RETURNING id::text`,
		`DELETE FROM organization.external_identities WHERE id = $1`, other, f.pfx+"-s2", f.pfx+"-dup")
	f.insert(`INSERT INTO organization.external_identities(user_id, provider_key, external_subject, username) VALUES ($1,'p3',$2,$3) RETURNING id::text`,
		`DELETE FROM organization.external_identities WHERE id = $1`, u, f.pfx+"-s3", f.pfx+"-dup")
	// The same user with two identities of one username is still unique.
	f.insert(`INSERT INTO organization.external_identities(user_id, provider_key, external_subject, username) VALUES ($1,'p4',$2,$3) RETURNING id::text`,
		`DELETE FROM organization.external_identities WHERE id = $1`, other, f.pfx+"-s4", f.pfx+"-twice")
	f.insert(`INSERT INTO organization.external_identities(user_id, provider_key, external_subject, username) VALUES ($1,'p5',$2,$3) RETURNING id::text`,
		`DELETE FROM organization.external_identities WHERE id = $1`, other, f.pfx+"-s5", f.pfx+"-twice")

	// An identity that disappeared from the directory no longer resolves.
	gone := f.user(f.pfx+"-gone", "active")
	f.insert(`INSERT INTO organization.external_identities(user_id, provider_key, external_subject, username, deleted_observed_at) VALUES ($1,'p6',$2,$3, now()) RETURNING id::text`,
		`DELETE FROM organization.external_identities WHERE id = $1`, gone, f.pfx+"-s6", f.pfx+"-removed")

	for _, tc := range []struct {
		ref   string
		want  string
		found bool
	}{
		{u, u, true},
		{strings.ToUpper(u), u, true},
		{"00000000-0000-7000-8000-000000000001", "", false},
		{f.pfx + "-alice", u, true},
		{f.pfx + "-ALICE", u, true},
		{f.pfx + "-dup", "", false},
		{f.pfx + "-twice", other, true},
		{strings.ToLower(f.pfx) + "-mail@example.test", byEmail, true},
		{strings.ToUpper(f.pfx) + "-MAIL@EXAMPLE.TEST", byEmail, true},
		{f.pfx + "-removed", "", false},
		{f.pfx + "-nobody", "", false},
		{"", "", false},
		{"nobody@example.test", "", false},
	} {
		id, found, err := svc.FindUser(ctx, tc.ref)
		if err != nil || found != tc.found || id != tc.want {
			t.Fatalf("FindUser(%q) = %q, %v, %v; want %q, %v", tc.ref, id, found, err, tc.want, tc.found)
		}
	}
}

func TestDirectoryGraphLookups(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	child, mid, top, gone := f.group(f.pfx+"-child", nil), f.group(f.pfx+"-mid", nil), f.group(f.pfx+"-top", nil), f.group(f.pfx+"-gone", nil)
	f.nest(mid, child, false)
	f.nest(top, mid, false)
	f.nest(gone, top, true)   // ended edge
	f.nest(child, top, false) // cycle top -> child
	u1, u2 := f.user(f.pfx+"-u1", "active"), f.user(f.pfx+"-u2", "active")
	f.member(child, u1, false)
	f.member(child, u2, true) // ended
	f.member(top, u1, false)

	byExt, trunc, err := f.repo.GroupsByExternalIDs(ctx, "test", []string{f.pfx + "-top", f.pfx + "-missing"}, 100)
	if err != nil || trunc || len(byExt) != 1 || byExt[0].ID != top || byExt[0].DisplayName != f.pfx+"-top" {
		t.Fatalf("by external id = %+v, %v, %v", byExt, trunc, err)
	}
	if other, _, err := f.repo.GroupsByExternalIDs(ctx, "other-provider", []string{f.pfx + "-top"}, 100); err != nil || len(other) != 0 {
		t.Fatalf("another provider key must not see the group: %+v, %v", other, err)
	}
	up, trunc, err := f.repo.NestingUp(ctx, "test", []string{child}, 100)
	if err != nil || trunc {
		t.Fatalf("up: %v, truncated=%v", err, trunc)
	}
	parents := map[string]bool{}
	for _, e := range up {
		parents[e.ParentID] = true
	}
	if !parents[mid] || !parents[top] || parents[gone] {
		t.Errorf("up edges = %+v (ended edge must not count)", up)
	}
	down, _, err := f.repo.NestingDown(ctx, "test", []string{top}, 100)
	if err != nil || len(down) < 2 {
		t.Fatalf("down edges = %+v, %v", down, err)
	}
	if limited, trunc, _ := f.repo.NestingUp(ctx, "test", []string{child}, 1); len(limited) != 1 || !trunc {
		t.Errorf("limit not applied or truncation not reported: %d, %v", len(limited), trunc)
	}
	if none, _, _ := f.repo.NestingUp(ctx, "other-provider", []string{child}, 100); len(none) != 0 {
		t.Errorf("another provider key must not see nesting: %+v", none)
	}
	ms, trunc, err := f.repo.UserMemberships(ctx, "test", []string{u1, u2}, 100)
	if err != nil || trunc || len(ms) != 2 {
		t.Fatalf("user memberships = %+v, %v, %v (ended interval must not count)", ms, trunc, err)
	}
	if cutMs, trunc, _ := f.repo.UserMemberships(ctx, "test", []string{u1, u2}, 1); len(cutMs) != 1 || !trunc {
		t.Errorf("membership limit: %d, truncated=%v", len(cutMs), trunc)
	}
	members, trunc, err := f.repo.GroupMembers(ctx, "test", []string{child}, 100)
	if err != nil || trunc || len(members) != 1 || members[0].UserID != u1 {
		t.Fatalf("group members = %+v, %v", members, err)
	}
	if cutMs, trunc, _ := f.repo.GroupMembers(ctx, "test", []string{child, top}, 1); len(cutMs) != 1 || !trunc {
		t.Errorf("group member limit: %d, truncated=%v", len(cutMs), trunc)
	}
	// The public wrapper ignores malformed ids.
	g := application.NewDirectoryGraph(f.repo)
	if got, trunc, err := g.GroupsByIDs(ctx, "test", []string{"not-a-uuid"}, 10); err != nil || got != nil || trunc {
		t.Errorf("malformed id: %v, %v", got, err)
	}
}

func TestDirectoryGraphDepthCapAndIdentity(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// A chain g0 <- g1 <- ... <- g12: walking up from g12 passes more than MaxNestingDepth levels.
	var chain []string
	for i := 0; i <= application.MaxNestingDepth+2; i++ {
		chain = append(chain, f.group(fmt.Sprintf("%s-chain-%02d", f.pfx, i), nil))
	}
	for i := 1; i < len(chain); i++ {
		f.nest(chain[i-1], chain[i], false) // chain[i-1] is the parent of chain[i]
	}
	up, trunc, err := f.repo.NestingUp(ctx, "test", []string{chain[len(chain)-1]}, 100)
	if err != nil || !trunc || len(up) != application.MaxNestingDepth {
		t.Fatalf("deep walk: %d edges, truncated=%v, %v; want %d edges and truncated", len(up), trunc, err, application.MaxNestingDepth)
	}
	shallow, trunc, err := f.repo.NestingUp(ctx, "test", []string{chain[3]}, 100)
	if err != nil || trunc || len(shallow) != 3 {
		t.Fatalf("shallow walk: %d edges, truncated=%v, %v", len(shallow), trunc, err)
	}

	withID, withoutID, gone := f.user(f.pfx+"-id", "active"), f.user(f.pfx+"-noid", "active"), f.user(f.pfx+"-idgone", "active")
	f.insert(`INSERT INTO organization.external_identities(user_id, provider_key, external_subject, username) VALUES ($1,'test',$2,$3) RETURNING id::text`,
		`DELETE FROM organization.external_identities WHERE id = $1`, withID, f.pfx+"-sub1", f.pfx+"-idname")
	f.insert(`INSERT INTO organization.external_identities(user_id, provider_key, external_subject, username, deleted_observed_at) VALUES ($1,'test',$2,$3, now()) RETURNING id::text`,
		`DELETE FROM organization.external_identities WHERE id = $1`, gone, f.pfx+"-sub2", f.pfx+"-idgonename")
	g := application.NewDirectoryGraph(f.repo)
	got, err := g.UsersWithIdentity(ctx, "test", []string{withID, withoutID, gone, "bad"})
	if err != nil || !got[withID] || got[withoutID] || got[gone] {
		t.Fatalf("users with identity = %v, %v", got, err)
	}
	if other, _ := g.UsersWithIdentity(ctx, "other-provider", []string{withID}); other[withID] {
		t.Errorf("identity of another provider key must not count")
	}
}
