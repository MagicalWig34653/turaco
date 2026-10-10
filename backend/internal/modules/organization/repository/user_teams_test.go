package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
)

// Member counts come from one grouped query for the whole page; ended memberships do not count.
func TestTeamListsCarryMemberCounts(t *testing.T) {
	f := newPeopleFix(t)
	ctx := context.Background()
	full := f.createTeam(f.pfx + " Full")
	empty := f.createTeam(f.pfx + " Empty")
	lead := f.local("CountLead", "organization.teams.manage")
	a, b, gone := f.local("CountA"), f.local("CountB"), f.local("CountGone")
	for _, u := range []string{a.ID, b.ID, gone.ID} {
		if _, err := f.repo.AddTeamMember(ctx, f.caller(lead.ID), full.ID, u, nil); err != nil {
			t.Fatalf("add member: %v", err)
		}
	}
	if err := f.repo.RemoveTeamMember(ctx, f.caller(lead.ID), full.ID, gone.ID); err != nil {
		t.Fatalf("remove member: %v", err)
	}
	res, err := f.repo.ListTeams(ctx, application.NameFilter{Query: f.pfx, Page: application.Page{Limit: 50}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, tm := range res.Items {
		if tm.MemberCount == nil {
			t.Fatalf("team %s has no member count", tm.Name)
		}
		got[tm.ID] = *tm.MemberCount
	}
	if got[full.ID] != 2 || got[empty.ID] != 0 {
		t.Errorf("counts: %v (full %s, empty %s)", got, full.ID, empty.ID)
	}
}

func TestListUserTeamsReturnsCurrentMembershipsWithRole(t *testing.T) {
	f := newPeopleFix(t)
	ctx := context.Background()
	t1 := f.createTeam(f.pfx + " One")
	t2 := f.createTeam(f.pfx + " Two")
	t3 := f.createTeam(f.pfx + " Three")
	lead := f.local("UTLead", "organization.teams.manage")
	user := f.local("UTUser")
	for _, tm := range []string{t1.ID, t2.ID, t3.ID} {
		if _, err := f.repo.AddTeamMember(ctx, f.caller(lead.ID), tm, user.ID, nil); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	if _, err := f.repo.SetMemberRole(ctx, f.caller(lead.ID), t2.ID, user.ID, "lead"); err != nil {
		t.Fatalf("lead: %v", err)
	}
	if err := f.repo.RemoveTeamMember(ctx, f.caller(lead.ID), t3.ID, user.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	page, err := f.repo.ListUserTeams(ctx, user.ID, application.Page{Limit: 1})
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("first page: %+v %v", page, err)
	}
	rest, err := f.repo.ListUserTeams(ctx, user.ID, application.Page{Limit: 10, Cursor: page.NextCursor})
	if err != nil || len(rest.Items) != 1 || rest.NextCursor != "" {
		t.Fatalf("second page: %+v %v", rest, err)
	}
	roles := map[string]string{}
	for _, m := range append(page.Items, rest.Items...) {
		r := "member"
		if m.Role != nil {
			r = *m.Role
		}
		roles[m.TeamID] = r
	}
	if len(roles) != 2 || roles[t2.ID] != "lead" || roles[t3.ID] != "" {
		t.Errorf("memberships: %v", roles)
	}
	if _, err := f.repo.ListUserTeams(ctx, "00000000-0000-7000-8000-00000000dead", application.Page{}); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("unknown user: %v", err)
	}
	if _, err := f.repo.ListUserTeams(ctx, "not-a-uuid", application.Page{}); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("malformed id: %v", err)
	}
}
