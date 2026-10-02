package repository

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

func testCaller(f *fixture) application.Caller {
	return application.Caller{Actor: audit.SystemActor("test"), CorrelationID: "corr-" + f.pfx}
}

// createTeam creates a Team through the repository and removes it (with its
// memberships) when the test ends.
func (f *fixture) createTeam(name string) application.Team {
	f.t.Helper()
	tm, err := f.repo.CreateTeam(context.Background(), testCaller(f), name)
	if err != nil {
		f.t.Fatalf("create team: %v", err)
	}
	f.t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM organization.team_memberships WHERE team_id = $1`, tm.ID)
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM organization.teams WHERE id = $1`, tm.ID)
	})
	return tm
}

func (f *fixture) auditActions(targetID string) []string {
	f.t.Helper()
	rows, err := f.pool.Query(context.Background(),
		`SELECT action FROM platform.audit_events WHERE target_type = 'team' AND target_id = $1 ORDER BY id`, targetID)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestTeamLifecycleIsAudited(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := testCaller(f)

	tm := f.createTeam(f.pfx + " Service Desk")
	if !tm.Active {
		t.Fatal("new team must be active")
	}
	renamed, err := f.repo.RenameTeam(ctx, c, tm.ID, f.pfx+" Helpdesk")
	if err != nil || renamed.Name != f.pfx+" Helpdesk" {
		t.Fatalf("rename: %+v %v", renamed, err)
	}
	if _, err := f.repo.RenameTeam(ctx, c, tm.ID, f.pfx+" Helpdesk"); err != nil {
		t.Fatalf("renaming to the same name must be a no-op: %v", err)
	}
	if _, err := f.repo.SetTeamActive(ctx, c, tm.ID, true); !errors.Is(err, application.ErrConflict) {
		t.Errorf("activating an active team = %v, want ErrConflict", err)
	}
	if off, err := f.repo.SetTeamActive(ctx, c, tm.ID, false); err != nil || off.Active {
		t.Fatalf("deactivate: %+v %v", off, err)
	}
	if on, err := f.repo.SetTeamActive(ctx, c, tm.ID, true); err != nil || !on.Active {
		t.Fatalf("activate: %+v %v", on, err)
	}
	want := []string{"organization.team.created", "organization.team.renamed", "organization.team.deactivated", "organization.team.activated"}
	if got := f.auditActions(tm.ID); !equalStrings(got, want) {
		t.Errorf("audit actions = %v, want %v", got, want)
	}
}

func TestTeamNamesAreUniqueAmongActiveTeams(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := testCaller(f)
	a := f.createTeam(f.pfx + " Network")
	if _, err := f.repo.CreateTeam(ctx, c, f.pfx+" NETWORK"); !errors.Is(err, application.ErrConflict) {
		t.Errorf("duplicate name (case-insensitive) = %v, want ErrConflict", err)
	}
	other := f.createTeam(f.pfx + " Other")
	if _, err := f.repo.RenameTeam(ctx, c, other.ID, f.pfx+" network"); !errors.Is(err, application.ErrConflict) {
		t.Errorf("rename to existing name = %v, want ErrConflict", err)
	}
	// A deactivated team frees its name; reactivating then conflicts.
	if _, err := f.repo.SetTeamActive(ctx, c, a.ID, false); err != nil {
		t.Fatal(err)
	}
	b := f.createTeam(f.pfx + " Network")
	if _, err := f.repo.SetTeamActive(ctx, c, a.ID, true); !errors.Is(err, application.ErrConflict) {
		t.Errorf("reactivating a team whose name is taken = %v, want ErrConflict", err)
	}
	_ = b
}

func TestTeamMembership(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := testCaller(f)
	active := f.user(f.pfx+" Active", "active") // users first: their cleanup runs after the team's
	departed := f.user(f.pfx+" Departed", "departed")
	tm := f.createTeam(f.pfx + " Team")
	role := "lead"

	m, err := f.repo.AddTeamMember(ctx, c, tm.ID, active, &role)
	if err != nil || m.UserID != active || m.Source != "platform" || m.DisplayName != f.pfx+" Active" {
		t.Fatalf("add member: %+v %v", m, err)
	}
	if _, err := f.repo.AddTeamMember(ctx, c, tm.ID, active, nil); !errors.Is(err, application.ErrConflict) {
		t.Errorf("adding a current member again = %v, want ErrConflict", err)
	}
	if _, err := f.repo.AddTeamMember(ctx, c, tm.ID, departed, nil); !errors.Is(err, application.ErrUserNotActive) {
		t.Errorf("adding a departed user = %v, want ErrUserNotActive", err)
	}
	if _, err := f.repo.AddTeamMember(ctx, c, tm.ID, missingID, nil); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("adding an unknown user = %v, want ErrNotFound", err)
	}
	if _, err := f.repo.AddTeamMember(ctx, c, missingID, active, nil); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("adding to an unknown team = %v, want ErrNotFound", err)
	}
	members, err := f.repo.ListTeamMembers(ctx, tm.ID, application.Page{})
	if err != nil || len(members.Items) != 1 || members.Items[0].UserID != active {
		t.Fatalf("members = %+v %v", members, err)
	}

	if err := f.repo.RemoveTeamMember(ctx, c, tm.ID, active); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := f.repo.RemoveTeamMember(ctx, c, tm.ID, active); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("removing a non-member = %v, want ErrNotFound", err)
	}
	if members, _ := f.repo.ListTeamMembers(ctx, tm.ID, application.Page{}); len(members.Items) != 0 {
		t.Errorf("members after removal = %d, want 0", len(members.Items))
	}
	// Re-adding after removal starts a new interval.
	if _, err := f.repo.AddTeamMember(ctx, c, tm.ID, active, nil); err != nil {
		t.Errorf("re-adding a removed member: %v", err)
	}

	off := f.createTeam(f.pfx + " Off")
	if _, err := f.repo.SetTeamActive(ctx, c, off.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.AddTeamMember(ctx, c, off.ID, active, nil); !errors.Is(err, application.ErrTeamInactive) {
		t.Errorf("adding to an inactive team = %v, want ErrTeamInactive", err)
	}
	wantActions := []string{"organization.team.created", "organization.team.member_added", "organization.team.member_removed", "organization.team.member_added"}
	if got := f.auditActions(tm.ID); !equalStrings(got, wantActions) {
		t.Errorf("audit actions = %v, want %v", got, wantActions)
	}
}

func TestConcurrentAddMemberCreatesOneMembership(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.user(f.pfx+" Racer", "active")
	tm := f.createTeam(f.pfx + " Race")

	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.repo.AddTeamMember(ctx, testCaller(f), tm.ID, u, nil)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	var ok, conflict int
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, application.ErrConflict):
			conflict++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 || conflict != 7 {
		t.Errorf("ok=%d conflict=%d, want 1 and 7", ok, conflict)
	}
}
