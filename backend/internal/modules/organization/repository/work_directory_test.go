package repository

import (
	"context"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
)

func TestWorkDirectory(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	wd := application.NewWorkDirectory(f.repo)

	active := f.user(f.pfx+" Active", "active")
	departed := f.user(f.pfx+" Departed", "departed")
	team := f.createTeam(f.pfx + " Team")
	off := f.createTeam(f.pfx + " Off")
	c := testCaller(f)
	for _, id := range []string{team.ID, off.ID} {
		if _, err := f.repo.AddTeamMember(ctx, c, id, active, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.repo.SetTeamActive(ctx, c, off.ID, false); err != nil {
		t.Fatal(err)
	}

	users, err := wd.ActiveUsers(ctx, []string{active, departed, missingID, "garbage"})
	if err != nil || !users[active] || users[departed] || users[missingID] || len(users) != 1 {
		t.Errorf("active users = %v %v", users, err)
	}
	names, err := wd.UserNames(ctx, []string{active, departed, "garbage"})
	if err != nil || names[active] != f.pfx+" Active" || names[departed] != f.pfx+" Departed" || len(names) != 2 {
		t.Errorf("user names = %v %v", names, err)
	}
	teams, err := wd.ActiveTeams(ctx, []string{team.ID, off.ID})
	if err != nil || !teams[team.ID] || teams[off.ID] {
		t.Errorf("active teams = %v %v", teams, err)
	}
	tn, err := wd.TeamNames(ctx, []string{team.ID, off.ID})
	if err != nil || len(tn) != 2 {
		t.Errorf("team names = %v %v", tn, err)
	}
	ids, err := wd.CurrentTeamIDs(ctx, active)
	if err != nil || len(ids) != 1 || ids[0] != team.ID {
		t.Errorf("current teams = %v %v (inactive teams must be excluded)", ids, err)
	}
	if ids, err := wd.CurrentTeamIDs(ctx, "garbage"); err != nil || len(ids) != 0 {
		t.Errorf("malformed user = %v %v", ids, err)
	}
	// An ended membership no longer counts.
	if err := f.repo.RemoveTeamMember(ctx, c, team.ID, active); err != nil {
		t.Fatal(err)
	}
	if ids, _ := wd.CurrentTeamIDs(ctx, active); len(ids) != 0 {
		t.Errorf("ended membership still counted: %v", ids)
	}
}
