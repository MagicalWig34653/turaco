package views_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/views"
)

func wantErr(t *testing.T, name string, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Errorf("%s: error = %v, want %v", name, err, target)
	}
}

func wantInvalid(t *testing.T, name string, err error) {
	t.Helper()
	var inv *views.InvalidError
	if !errors.As(err, &inv) {
		t.Errorf("%s: error = %v, want an invalid-request error", name, err)
	}
}

func TestCreateGetListBasics(t *testing.T) {
	e := newEnv(t)
	owner := e.newUser()
	v := e.create(owner, "  Open tickets ", statusFilter(t))
	if v.Name != "Open tickets" || v.Version != 1 || v.Access != views.AccessOwner || v.Visibility != views.VisibilityPrivate || !v.ModuleEnabled {
		t.Fatalf("unexpected view %+v", v)
	}
	if v.Definition.Filter == nil || v.Definition.Filter.V != 1 {
		t.Fatalf("definition lost: %+v", v.Definition)
	}
	got, err := e.svc.Get(ctx, caller(owner), v.ID)
	if err != nil || got.ID != v.ID || got.OwnerName == "" {
		t.Fatalf("get: %v %+v", err, got)
	}
	list, err := e.svc.List(ctx, caller(owner), views.ListInput{})
	if err != nil || len(list.Items) != 1 || list.Items[0].ID != v.ID {
		t.Fatalf("list: %v %+v", err, list)
	}
	// Another user sees nothing of it.
	other := e.newUser()
	list, err = e.svc.List(ctx, caller(other), views.ListInput{})
	if err != nil || len(list.Items) != 0 {
		t.Fatalf("other list: %v %+v", err, list)
	}
	// The dry run saw the filter once, with a one-row limit.
	if e.runner.callCount() != 1 || e.runner.lastCall().Limit != 1 {
		t.Errorf("validation dry run: %d calls, last %+v", e.runner.callCount(), e.runner.lastCall())
	}
}

func TestCreateValidation(t *testing.T) {
	e := newEnv(t)
	u := e.newUser()
	create := func(in views.CreateInput, perms ...string) error {
		_, err := e.svc.Create(ctx, caller(u, perms...), in)
		return err
	}
	ok := views.CreateInput{Resource: "tickets", Name: "x"}
	cases := map[string]views.CreateInput{
		"empty name":      {Resource: "tickets", Name: "   "},
		"long name":       {Resource: "tickets", Name: strings.Repeat("n", 81)},
		"bidi name":       {Resource: "tickets", Name: "a‮b"},
		"control name":    {Resource: "tickets", Name: "a\x00b"},
		"unknown":         {Resource: "payroll", Name: "x"},
		"long desc":       {Resource: "tickets", Name: "x", Description: strings.Repeat("d", 501)},
		"unsafe desc":     {Resource: "tickets", Name: "x", Description: "a​b"},
		"column injected": {Resource: "tickets", Name: "x", Definition: views.Definition{Columns: []string{"title; DROP TABLE x"}}},
		"column repeated": {Resource: "tickets", Name: "x", Definition: views.Definition{Columns: []string{"title", "title"}}},
		"many columns":    {Resource: "tickets", Name: "x", Definition: views.Definition{Columns: manyColumns(views.MaxColumns + 1)}},
		"filter version":  {Resource: "tickets", Name: "x", Definition: views.Definition{Filter: &query.Filter{V: 2}}},
	}
	for name, in := range cases {
		wantInvalid(t, name, create(in))
	}
	if err := create(ok); err != nil {
		t.Fatalf("valid create: %v", err)
	}
	// A resource the caller may not read.
	wantErr(t, "tasks without permission", create(views.CreateInput{Resource: "tasks", Name: "t"}), views.ErrForbidden)
	if err := create(views.CreateInput{Resource: "tasks", Name: "t"}, "tasks.work"); err != nil {
		t.Errorf("tasks with permission: %v", err)
	}
	// A name is unique per owner and resource, case-insensitively.
	wantErr(t, "duplicate name", create(views.CreateInput{Resource: "tickets", Name: "X"}), views.ErrNameTaken)
	// The module's own validation is authoritative: its rejection is passed through unchanged.
	e.runner.queryFn = func(views.Caller, string, query.Request) (views.Result, error) {
		return views.Result{}, &views.RunError{Status: 400, Code: "query.invalid_filter", Message: "Unknown or unavailable field."}
	}
	var re *views.RunError
	if err := create(views.CreateInput{Resource: "tickets", Name: "bad", Definition: statusFilter(t)}); !errors.As(err, &re) || re.Code != "query.invalid_filter" {
		t.Errorf("module validation error not passed through: %v", err)
	}
	// An oversized document is refused before anything runs.
	e.runner.queryFn = nil
	big := fmt.Sprintf(`{"v":1,"search":%q}`, strings.Repeat("a", 20000))
	var f query.Filter
	_ = json.Unmarshal([]byte(big), &f)
	if err := create(views.CreateInput{Resource: "tickets", Name: "big", Definition: views.Definition{Filter: &f}}); err == nil {
		t.Error("an oversized definition was accepted")
	}
}

func manyColumns(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("c%d", i)
	}
	return out
}

func TestOwnerLimit(t *testing.T) {
	e := newEnv(t)
	u := e.newUser()
	if _, err := e.pool.Exec(ctx, `
		INSERT INTO views.saved_views (resource, name, owner_user_id, definition, definition_hash)
		SELECT 'tickets', 'bulk ' || g, $1::uuid, '{}'::jsonb, repeat('0', 64) FROM generate_series(1, $2) g`, u, views.MaxViewsPerOwner); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.Create(ctx, caller(u), views.CreateInput{Resource: "tickets", Name: "one too many"})
	wantErr(t, "limit", err, views.ErrLimitReached)
}

// ---------------------------------------------------------------- IDOR and share matrix

func TestPrivateViewIsInvisibleToOthers(t *testing.T) {
	e := newEnv(t)
	owner, other := e.newUser(), e.newUser()
	v := e.create(owner, "mine", statusFilter(t))
	oc := caller(other)
	unknown := e.newID()
	queriesBefore := e.runner.callCount()
	// Every operation answers exactly like it does for an id that does not exist.
	checks := map[string]func(id string) error{
		"get": func(id string) error { _, err := e.svc.Get(ctx, oc, id); return err },
		"update": func(id string) error {
			n := "x"
			_, err := e.svc.Update(ctx, oc, id, views.UpdateInput{ExpectedVersion: 1, Name: &n})
			return err
		},
		"archive":   func(id string) error { _, err := e.svc.Archive(ctx, oc, id, 1); return err },
		"restore":   func(id string) error { _, err := e.svc.Restore(ctx, oc, id, 1); return err },
		"shares":    func(id string) error { _, err := e.svc.SetShares(ctx, oc, id, 1, nil); return err },
		"results":   func(id string) error { _, err := e.svc.Results(ctx, oc, id, views.ResultsInput{}); return err },
		"duplicate": func(id string) error { _, err := e.svc.Duplicate(ctx, oc, id); return err },
		"takeover":  func(id string) error { _, err := e.svc.TakeOver(ctx, oc, id, 1); return err },
		"pin": func(id string) error {
			return e.svc.ReplacePins(ctx, oc, []views.PinInput{{ViewID: id, GroupKey: "tickets"}})
		},
	}
	for name, fn := range checks {
		a, b := fn(v.ID), fn(unknown)
		wantErr(t, name+" (private view)", a, views.ErrNotFound)
		wantErr(t, name+" (unknown view)", b, views.ErrNotFound)
	}
	wantErr(t, "invalid id", checks["get"]("not-a-uuid"), views.ErrNotFound)
	if e.runner.callCount() != queriesBefore {
		t.Error("a query ran for a caller without access")
	}
	// The owner still has everything.
	if _, err := e.svc.Get(ctx, caller(owner), v.ID); err != nil {
		t.Errorf("owner get: %v", err)
	}
}

func TestShareMatrix(t *testing.T) {
	e := newEnv(t)
	owner, viewer, editor, outsider, teamMate, roleHolder := e.newUser(), e.newUser(), e.newUser(), e.newUser(), e.newUser(), e.newUser()
	team, role := e.newTeam(), e.newRole()
	e.dir.setTeams(teamMate, team)
	e.assignRole(role, roleHolder)
	v := e.create(owner, "shared", statusFilter(t))
	v = e.share(owner, v, []string{views.PermShare}, views.ShareInput{SubjectType: "user", SubjectID: viewer, Level: "use"},
		views.ShareInput{SubjectType: "user", SubjectID: editor, Level: "edit"},
		views.ShareInput{SubjectType: "team", SubjectID: team, Level: "use"},
		views.ShareInput{SubjectType: "role", SubjectID: role, Level: "use"})
	if v.Visibility != views.VisibilityShared || v.Version != 2 || len(v.Shares) != 4 {
		t.Fatalf("after share: %+v", v)
	}
	access := func(u string) (string, error) {
		got, err := e.svc.Get(ctx, caller(u), v.ID)
		return got.Access, err
	}
	for u, want := range map[string]string{viewer: "use", editor: "edit", teamMate: "use", roleHolder: "use"} {
		if got, err := access(u); err != nil || got != want {
			t.Errorf("access of %s = %q, %v; want %q", u[:4], got, err, want)
		}
	}
	if _, err := access(outsider); !errors.Is(err, views.ErrNotFound) {
		t.Errorf("outsider: %v", err)
	}
	// Non-owners never see the share list.
	if got, _ := e.svc.Get(ctx, caller(viewer), v.ID); len(got.Shares) != 0 {
		t.Error("a viewer saw the share list")
	}
	// A use-level viewer may run and duplicate but not change anything.
	if _, err := e.svc.Results(ctx, caller(viewer), v.ID, views.ResultsInput{}); err != nil {
		t.Errorf("viewer results: %v", err)
	}
	n := "renamed"
	_, err := e.svc.Update(ctx, caller(viewer), v.ID, views.UpdateInput{ExpectedVersion: v.Version, Name: &n})
	wantErr(t, "use-level update", err, views.ErrForbidden)
	_, err = e.svc.Archive(ctx, caller(viewer), v.ID, v.Version)
	wantErr(t, "use-level archive", err, views.ErrForbidden)
	_, err = e.svc.SetShares(ctx, caller(viewer, views.PermShare), v.ID, v.Version, nil)
	wantErr(t, "use-level share change", err, views.ErrForbidden)
	if _, err := e.svc.Duplicate(ctx, caller(viewer), v.ID); err != nil {
		t.Errorf("viewer duplicate: %v", err)
	}
	// An edit-level user changes the definition and name, but not the shares, the archive state or the owner.
	upd, err := e.svc.Update(ctx, caller(editor), v.ID, views.UpdateInput{ExpectedVersion: v.Version, Name: &n})
	if err != nil || upd.Version != v.Version+1 || upd.LastEditedBy != editor {
		t.Errorf("editor update: %v %+v", err, upd)
	}
	_, err = e.svc.SetShares(ctx, caller(editor, views.PermShare), v.ID, upd.Version, nil)
	wantErr(t, "editor share change", err, views.ErrForbidden)
	_, err = e.svc.Archive(ctx, caller(editor), v.ID, upd.Version)
	wantErr(t, "editor archive", err, views.ErrForbidden)
	_, err = e.svc.TakeOver(ctx, caller(editor), v.ID, upd.Version)
	wantErr(t, "editor take over", err, views.ErrForbidden)
	// Shared Views show up in the "shared with me" list of the viewer and not of the outsider.
	list, err := e.svc.List(ctx, caller(viewer), views.ListInput{Scope: "shared"})
	if err != nil || len(list.Items) != 1 || list.Items[0].Access != "use" {
		t.Errorf("shared list: %v %+v", err, list)
	}
	list, _ = e.svc.List(ctx, caller(outsider), views.ListInput{})
	if len(list.Items) != 0 {
		t.Error("outsider listed a shared view")
	}
}

func TestSharePermissionsAndValidation(t *testing.T) {
	e := newEnv(t)
	owner, target := e.newUser(), e.newUser()
	team := e.newTeam()
	v := e.create(owner, "v", views.Definition{})
	set := func(perms []string, ver int, in ...views.ShareInput) error {
		_, err := e.svc.SetShares(ctx, caller(owner, perms...), v.ID, ver, in)
		return err
	}
	user := views.ShareInput{SubjectType: "user", SubjectID: target, Level: "use"}
	every := views.ShareInput{SubjectType: "everyone", Level: "use"}
	wantErr(t, "share without views.share", set(nil, 1, user), views.ErrForbidden)
	wantErr(t, "everyone with only views.share", set([]string{views.PermShare}, 1, every), views.ErrForbidden)
	wantErr(t, "views.publish alone does not allow user shares", set([]string{views.PermPublish}, 1, user), views.ErrForbidden)
	wantErr(t, "unknown user", set([]string{views.PermShare}, 1, views.ShareInput{SubjectType: "user", SubjectID: e.newID(), Level: "use"}), views.ErrSubjectNotFound)
	e.dir.deactivate(target)
	wantErr(t, "inactive user", set([]string{views.PermShare}, 1, user), views.ErrSubjectNotFound)
	wantErr(t, "unknown team", set([]string{views.PermShare}, 1, views.ShareInput{SubjectType: "team", SubjectID: e.newID(), Level: "use"}), views.ErrSubjectNotFound)
	wantErr(t, "unknown role", set([]string{views.PermShare}, 1, views.ShareInput{SubjectType: "role", SubjectID: e.newID(), Level: "use"}), views.ErrSubjectNotFound)
	wantInvalid(t, "everyone with edit", set([]string{views.PermPublish}, 1, views.ShareInput{SubjectType: "everyone", Level: "edit"}))
	wantInvalid(t, "everyone with subject", set([]string{views.PermPublish}, 1, views.ShareInput{SubjectType: "everyone", SubjectID: target, Level: "use"}))
	wantInvalid(t, "bad level", set([]string{views.PermShare}, 1, views.ShareInput{SubjectType: "team", SubjectID: team, Level: "own"}))
	wantInvalid(t, "bad type", set([]string{views.PermShare}, 1, views.ShareInput{SubjectType: "group", SubjectID: team, Level: "use"}))
	wantInvalid(t, "bad id", set([]string{views.PermShare}, 1, views.ShareInput{SubjectType: "team", SubjectID: "x", Level: "use"}))
	wantInvalid(t, "duplicate subject", set([]string{views.PermShare}, 1, views.ShareInput{SubjectType: "team", SubjectID: team, Level: "use"}, views.ShareInput{SubjectType: "team", SubjectID: team, Level: "edit"}))
	wantInvalid(t, "owner as subject", set([]string{views.PermShare}, 1, views.ShareInput{SubjectType: "user", SubjectID: owner, Level: "use"}))
	wantInvalid(t, "missing version", set([]string{views.PermShare}, 0))
	tooMany := make([]views.ShareInput, views.MaxSharesPerView+1)
	for i := range tooMany {
		tooMany[i] = views.ShareInput{SubjectType: "team", SubjectID: e.newTeam(), Level: "use"}
	}
	wantErr(t, "too many shares", set([]string{views.PermShare}, 1, tooMany...), views.ErrLimitReached)

	// Publishing needs views.publish and is audited as a publication.
	out, err := e.svc.SetShares(ctx, caller(owner, views.PermPublish), v.ID, 1, []views.ShareInput{every})
	if err != nil || out.Visibility != views.VisibilityShared {
		t.Fatalf("publish: %v %+v", err, out)
	}
	if n := e.auditCount(v.ID, "views.published"); n != 1 {
		t.Errorf("views.published audit rows = %d", n)
	}
	// Anyone signed in can now use the view.
	stranger := e.newUser()
	if _, err := e.svc.Results(ctx, caller(stranger), v.ID, views.ResultsInput{}); err != nil {
		t.Errorf("stranger results of a published view: %v", err)
	}
	// Removing a share needs no permission (revocation is always allowed), adding back does.
	out, err = e.svc.SetShares(ctx, caller(owner), v.ID, out.Version, nil)
	if err != nil || out.Visibility != views.VisibilityPrivate || len(out.Shares) != 0 {
		t.Fatalf("unshare without permissions: %v %+v", err, out)
	}
	if _, err := e.svc.Get(ctx, caller(stranger), v.ID); !errors.Is(err, views.ErrNotFound) {
		t.Errorf("stranger after unshare: %v", err)
	}
	// An unchanged set neither bumps the version nor needs a permission.
	again, err := e.svc.SetShares(ctx, caller(owner), v.ID, out.Version, nil)
	if err != nil || again.Version != out.Version {
		t.Errorf("idempotent unshare: %v %+v", err, again)
	}
}

func (e *env) auditCount(viewID, action string) int {
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM platform.audit_events WHERE target_type = 'view' AND target_id = $1 AND action = $2`, viewID, action).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// ---------------------------------------------------------------- revocation, membership and races

func TestRevokedShareStopsEverythingAtOnce(t *testing.T) {
	e := newEnv(t)
	owner, viewer := e.newUser(), e.newUser()
	v := e.create(owner, "v", statusFilter(t))
	v = e.share(owner, v, []string{views.PermShare}, views.ShareInput{SubjectType: "user", SubjectID: viewer, Level: "use"})
	vc := caller(viewer)
	if err := e.svc.ReplacePins(ctx, vc, []views.PinInput{{ViewID: v.ID, GroupKey: "tickets"}}); err != nil {
		t.Fatal(err)
	}
	if sb, _ := e.svc.Sidebar(ctx, vc); len(sb.Groups) != 1 {
		t.Fatalf("sidebar before revoke: %+v", sb)
	}
	v = e.share(owner, v, nil)
	_, err := e.svc.Results(ctx, vc, v.ID, views.ResultsInput{})
	wantErr(t, "results after revoke", err, views.ErrNotFound)
	_, err = e.svc.Get(ctx, vc, v.ID)
	wantErr(t, "get after revoke", err, views.ErrNotFound)
	if sb, err := e.svc.Sidebar(ctx, vc); err != nil || len(sb.Groups) != 0 {
		t.Errorf("sidebar after revoke: %v %+v", err, sb)
	}
	if pins, _ := e.svc.Pins(ctx, vc); len(pins) != 0 {
		t.Errorf("pins after revoke: %+v", pins)
	}
	// The stale pin row cannot be used to pin it again either.
	wantErr(t, "re-pin", e.svc.ReplacePins(ctx, vc, []views.PinInput{{ViewID: v.ID, GroupKey: "tickets"}}), views.ErrNotFound)
	if n := e.auditCount(v.ID, "views.share.revoked"); n != 1 {
		t.Errorf("views.share.revoked audit rows = %d", n)
	}
}

func TestTeamAndRoleMembershipChangesApplyImmediately(t *testing.T) {
	e := newEnv(t)
	owner, member, holder := e.newUser(), e.newUser(), e.newUser()
	team, role := e.newTeam(), e.newRole()
	v := e.create(owner, "v", statusFilter(t))
	v = e.share(owner, v, []string{views.PermShare}, views.ShareInput{SubjectType: "team", SubjectID: team, Level: "use"},
		views.ShareInput{SubjectType: "role", SubjectID: role, Level: "use"})
	get := func(u string) error { _, err := e.svc.Get(ctx, caller(u), v.ID); return err }
	wantErr(t, "before joining", get(member), views.ErrNotFound)
	e.dir.setTeams(member, team)
	if err := get(member); err != nil {
		t.Errorf("after joining: %v", err)
	}
	e.dir.setTeams(member)
	wantErr(t, "after leaving", get(member), views.ErrNotFound)
	e.assignRole(role, holder)
	if err := get(holder); err != nil {
		t.Errorf("role holder: %v", err)
	}
	e.revokeRole(role, holder)
	wantErr(t, "role revoked", get(holder), views.ErrNotFound)
	// A deleted role stops resolving too.
	e.assignRole(role, holder)
	if _, err := e.pool.Exec(ctx, `UPDATE platform.roles SET deleted_at = now() WHERE id = $1::uuid`, role); err != nil {
		t.Fatal(err)
	}
	wantErr(t, "role deleted", get(holder), views.ErrNotFound)
}

func TestRevokeDuringExecutionDiscardsTheResult(t *testing.T) {
	e := newEnv(t)
	owner, viewer := e.newUser(), e.newUser()
	v := e.create(owner, "v", statusFilter(t))
	v = e.share(owner, v, []string{views.PermShare}, views.ShareInput{SubjectType: "user", SubjectID: viewer, Level: "use"})
	// The share is revoked while the module query runs.
	e.runner.queryFn = func(views.Caller, string, query.Request) (views.Result, error) {
		if _, err := e.svc.SetShares(ctx, caller(owner), v.ID, v.Version, nil); err != nil {
			t.Errorf("revoke in flight: %v", err)
		}
		return views.Result{Items: json.RawMessage(`[{"id":"leak"}]`)}, nil
	}
	out, err := e.svc.Results(ctx, caller(viewer), v.ID, views.ResultsInput{})
	wantErr(t, "revoked in flight", err, views.ErrNotFound)
	if len(out.Items) != 0 {
		t.Errorf("rows leaked after a revoke in flight: %s", out.Items)
	}
}

func TestEditDuringExecutionIsAConflict(t *testing.T) {
	e := newEnv(t)
	owner := e.newUser()
	v := e.create(owner, "v", statusFilter(t))
	e.runner.queryFn = func(views.Caller, string, query.Request) (views.Result, error) {
		n := "edited meanwhile"
		if _, err := e.svc.Update(ctx, caller(owner), v.ID, views.UpdateInput{ExpectedVersion: v.Version, Name: &n}); err != nil {
			t.Errorf("edit in flight: %v", err)
		}
		return views.Result{Items: json.RawMessage(`[]`)}, nil
	}
	_, err := e.svc.Results(ctx, caller(owner), v.ID, views.ResultsInput{})
	wantErr(t, "edit in flight", err, views.ErrConflict)
}

func TestArchiveDuringExecutionDiscardsTheResult(t *testing.T) {
	e := newEnv(t)
	owner, viewer := e.newUser(), e.newUser()
	v := e.create(owner, "v", statusFilter(t))
	v = e.share(owner, v, []string{views.PermShare}, views.ShareInput{SubjectType: "user", SubjectID: viewer, Level: "use"})
	e.runner.queryFn = func(views.Caller, string, query.Request) (views.Result, error) {
		if _, err := e.svc.Archive(ctx, caller(owner), v.ID, v.Version); err != nil {
			t.Errorf("archive in flight: %v", err)
		}
		return views.Result{Items: json.RawMessage(`[{"id":"leak"}]`)}, nil
	}
	out, err := e.svc.Results(ctx, caller(viewer), v.ID, views.ResultsInput{})
	wantErr(t, "archived in flight", err, views.ErrNotFound)
	if len(out.Items) != 0 {
		t.Errorf("rows leaked after an archive in flight: %s", out.Items)
	}
}

func TestConcurrentSharingAndEditingNeverBothWin(t *testing.T) {
	e := newEnv(t)
	owner, a, b := e.newUser(), e.newUser(), e.newUser()
	v := e.create(owner, "v", views.Definition{})
	oc := caller(owner, views.PermShare)
	errs := make(chan error, 2)
	go func() {
		_, err := e.svc.SetShares(ctx, oc, v.ID, v.Version, []views.ShareInput{{SubjectType: "user", SubjectID: a, Level: "use"}})
		errs <- err
	}()
	go func() {
		_, err := e.svc.SetShares(ctx, oc, v.ID, v.Version, []views.ShareInput{{SubjectType: "user", SubjectID: b, Level: "use"}})
		errs <- err
	}()
	var ok, conflict int
	for i := 0; i < 2; i++ {
		switch err := <-errs; {
		case err == nil:
			ok++
		case errors.Is(err, views.ErrConflict):
			conflict++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Errorf("concurrent share replacement: %d succeeded, %d conflicted; want exactly one of each", ok, conflict)
	}
	got, _ := e.svc.Get(ctx, oc, v.ID)
	if len(got.Shares) != 1 {
		t.Errorf("lost update: %d shares", len(got.Shares))
	}
}

// ---------------------------------------------------------------- versions and lifecycle

func TestVersionGuardsEveryLifecycleOperation(t *testing.T) {
	e := newEnv(t)
	owner := e.newUser()
	admin := e.newUser()
	v := e.create(owner, "v", statusFilter(t))
	oc := caller(owner)
	n := "new"
	_, err := e.svc.Update(ctx, oc, v.ID, views.UpdateInput{ExpectedVersion: 0, Name: &n})
	wantInvalid(t, "update without version", err)
	_, err = e.svc.Update(ctx, oc, v.ID, views.UpdateInput{ExpectedVersion: 7, Name: &n})
	wantErr(t, "update stale", err, views.ErrConflict)
	_, err = e.svc.Update(ctx, oc, v.ID, views.UpdateInput{ExpectedVersion: 1})
	wantInvalid(t, "update with nothing", err)
	for name, fn := range map[string]func(int) error{
		"archive": func(ver int) error { _, err := e.svc.Archive(ctx, oc, v.ID, ver); return err },
		"restore": func(ver int) error { _, err := e.svc.Restore(ctx, oc, v.ID, ver); return err },
		"shares":  func(ver int) error { _, err := e.svc.SetShares(ctx, oc, v.ID, ver, nil); return err },
		"takeover": func(ver int) error {
			_, err := e.svc.TakeOver(ctx, caller(admin, views.PermAdmin), v.ID, ver)
			return err
		},
	} {
		wantInvalid(t, name+" without version", fn(0))
		err := fn(99)
		if name == "restore" {
			wantInvalid(t, name+" of a live view", err) // the state precondition is checked first
			continue
		}
		wantErr(t, name+" stale", err, views.ErrConflict)
	}
	// Two editors with the same version: the second loses.
	first, err := e.svc.Update(ctx, oc, v.ID, views.UpdateInput{ExpectedVersion: 1, Name: &n})
	if err != nil || first.Version != 2 {
		t.Fatal(err, first.Version)
	}
	m := "other"
	_, err = e.svc.Update(ctx, oc, v.ID, views.UpdateInput{ExpectedVersion: 1, Name: &m})
	wantErr(t, "lost update", err, views.ErrConflict)
	// Archive, then every change is refused until restored.
	arch, err := e.svc.Archive(ctx, oc, v.ID, first.Version)
	if err != nil || arch.ArchivedAt == nil || arch.Version != 3 {
		t.Fatalf("archive: %v %+v", err, arch)
	}
	_, err = e.svc.Update(ctx, oc, v.ID, views.UpdateInput{ExpectedVersion: arch.Version, Name: &m})
	wantErr(t, "update archived", err, views.ErrArchived)
	_, err = e.svc.Archive(ctx, oc, v.ID, arch.Version)
	wantErr(t, "archive archived", err, views.ErrArchived)
	_, err = e.svc.Results(ctx, oc, v.ID, views.ResultsInput{})
	wantErr(t, "results archived", err, views.ErrArchived)
	if list, _ := e.svc.List(ctx, oc, views.ListInput{}); len(list.Items) != 0 {
		t.Error("archived view in the default list")
	}
	if list, _ := e.svc.List(ctx, oc, views.ListInput{Scope: "mine", Archived: true}); len(list.Items) != 1 {
		t.Error("archived view missing from the archive list")
	}
	rest, err := e.svc.Restore(ctx, oc, v.ID, arch.Version)
	if err != nil || rest.ArchivedAt != nil || rest.Version != 4 {
		t.Fatalf("restore: %v %+v", err, rest)
	}
	for _, a := range []string{"views.view.renamed", "views.view.archived", "views.view.restored", "views.view.created"} {
		if e.auditCount(v.ID, a) == 0 {
			t.Errorf("no audit row %s", a)
		}
	}
}

func TestArchivedViewsStopBeingShared(t *testing.T) {
	e := newEnv(t)
	owner, viewer := e.newUser(), e.newUser()
	v := e.create(owner, "v", statusFilter(t))
	v = e.share(owner, v, []string{views.PermShare}, views.ShareInput{SubjectType: "user", SubjectID: viewer, Level: "use"})
	if _, err := e.svc.Archive(ctx, caller(owner), v.ID, v.Version); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.Get(ctx, caller(viewer), v.ID)
	wantErr(t, "viewer of archived", err, views.ErrNotFound)
	_, err = e.svc.Results(ctx, caller(viewer), v.ID, views.ResultsInput{})
	wantErr(t, "viewer results of archived", err, views.ErrNotFound)
	if list, _ := e.svc.List(ctx, caller(viewer), views.ListInput{}); len(list.Items) != 0 {
		t.Error("archived shared view listed")
	}
}

func TestDeactivatedOwnerEndsTheSharingAndAdminCanTakeOver(t *testing.T) {
	e := newEnv(t)
	owner, viewer, admin := e.newUser(), e.newUser(), e.newUser()
	v := e.create(owner, "v", statusFilter(t))
	v = e.share(owner, v, []string{views.PermShare}, views.ShareInput{SubjectType: "user", SubjectID: viewer, Level: "use"})
	e.dir.deactivate(owner)
	_, err := e.svc.Results(ctx, caller(viewer), v.ID, views.ResultsInput{})
	wantErr(t, "viewer after owner left", err, views.ErrNotFound)
	// Only views.admin sees it, never runs it, and can take it over.
	ac := caller(admin, views.PermAdmin)
	info, err := e.svc.Get(ctx, ac, v.ID)
	if err != nil || info.Access != views.AccessAdmin || len(info.Shares) != 1 {
		t.Fatalf("admin get: %v %+v", err, info)
	}
	_, err = e.svc.Results(ctx, ac, v.ID, views.ResultsInput{})
	wantErr(t, "admin results without a share", err, views.ErrNotFound)
	_, err = e.svc.TakeOver(ctx, caller(admin), v.ID, v.Version)
	wantErr(t, "take over without views.admin", err, views.ErrNotFound)
	taken, err := e.svc.TakeOver(ctx, ac, v.ID, v.Version)
	if err != nil || taken.OwnerID != admin || taken.Version != v.Version+1 {
		t.Fatalf("take over: %v %+v", err, taken)
	}
	if e.auditCount(v.ID, "views.view.ownership_taken") != 1 {
		t.Error("take over not audited")
	}
	// The old share still works now that an active owner stands behind it.
	if _, err := e.svc.Results(ctx, caller(viewer), v.ID, views.ResultsInput{}); err != nil {
		t.Errorf("viewer after take over: %v", err)
	}
}

func TestAdminListAndUnshare(t *testing.T) {
	e := newEnv(t)
	owner, admin, stranger := e.newUser(), e.newUser(), e.newUser()
	v := e.create(owner, "private", statusFilter(t))
	_, err := e.svc.List(ctx, caller(stranger), views.ListInput{Scope: "admin"})
	wantErr(t, "admin scope without permission", err, views.ErrForbidden)
	list, err := e.svc.List(ctx, caller(admin, views.PermAdmin), views.ListInput{Scope: "admin"})
	found := false
	for _, it := range list.Items {
		found = found || it.ID == v.ID
	}
	if err != nil || !found {
		t.Fatalf("admin list: %v found=%v", err, found)
	}
	// An administrator can archive (and thereby unshare) but not edit the definition.
	n := "x"
	_, err = e.svc.Update(ctx, caller(admin, views.PermAdmin), v.ID, views.UpdateInput{ExpectedVersion: v.Version, Name: &n})
	wantErr(t, "admin edit", err, views.ErrForbidden)
	if _, err := e.svc.Archive(ctx, caller(admin, views.PermAdmin), v.ID, v.Version); err != nil {
		t.Errorf("admin archive: %v", err)
	}
}

func TestDuplicate(t *testing.T) {
	e := newEnv(t)
	owner, viewer := e.newUser(), e.newUser()
	v := e.create(owner, strings.Repeat("n", 80), statusFilter(t))
	v = e.share(owner, v, []string{views.PermShare}, views.ShareInput{SubjectType: "user", SubjectID: viewer, Level: "use"})
	cp, err := e.svc.Duplicate(ctx, caller(viewer), v.ID)
	if err != nil || cp.OwnerID != viewer || cp.Visibility != views.VisibilityPrivate || len(cp.Shares) != 0 || len([]rune(cp.Name)) > 80 {
		t.Fatalf("duplicate: %v %+v", err, cp)
	}
	cp2, err := e.svc.Duplicate(ctx, caller(viewer), v.ID)
	if err != nil || cp2.Name == cp.Name {
		t.Errorf("second duplicate must get another name: %v %q %q", err, cp.Name, cp2.Name)
	}
	if cp.Definition.Filter == nil || cp.Hash != v.Hash {
		t.Error("definition was not copied")
	}
}

func TestPurgeArchived(t *testing.T) {
	e := newEnv(t)
	owner := e.newUser()
	old := e.create(owner, "old", views.Definition{})
	fresh := e.create(owner, "fresh", views.Definition{})
	for _, id := range []string{old.ID, fresh.ID} {
		v, _ := e.svc.Get(ctx, caller(owner), id)
		if _, err := e.svc.Archive(ctx, caller(owner), id, v.Version); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.pool.Exec(ctx, `UPDATE views.saved_views SET archived_at = now() - interval '100 days' WHERE id = $1::uuid`, old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.PurgeArchived(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM views.saved_views WHERE id = ANY($1::text[]::uuid[])`, []string{old.ID, fresh.ID}).Scan(&n)
	if n != 1 {
		t.Errorf("after purge %d of the two views remain, want 1 (the recently archived one)", n)
	}
	if e.auditCount(old.ID, "views.view.purged") != 1 {
		t.Error("purge not audited")
	}
}

// ---------------------------------------------------------------- module switches and resource permissions

func TestModuleSwitch(t *testing.T) {
	e := newEnv(t)
	owner, viewer := e.newUser(), e.newUser()
	v := e.create(owner, "v", statusFilter(t))
	v = e.share(owner, v, []string{views.PermShare}, views.ShareInput{SubjectType: "user", SubjectID: viewer, Level: "use"})
	vc := caller(viewer)
	if err := e.svc.ReplacePins(ctx, vc, []views.PinInput{{ViewID: v.ID, GroupKey: "tickets"}}); err != nil {
		t.Fatal(err)
	}
	before := e.runner.callCount()
	e.gate.set(moduleTickets, false)
	_, err := e.svc.Results(ctx, vc, v.ID, views.ResultsInput{})
	wantErr(t, "results with the module off", err, views.ErrModuleDisabled)
	_, err = e.svc.Create(ctx, caller(owner), views.CreateInput{Resource: "tickets", Name: "n"})
	wantErr(t, "create with the module off", err, views.ErrModuleDisabled)
	n := "x"
	_, err = e.svc.Update(ctx, caller(owner), v.ID, views.UpdateInput{ExpectedVersion: v.Version, Name: &n})
	wantErr(t, "update with the module off", err, views.ErrModuleDisabled)
	_, err = e.svc.Duplicate(ctx, vc, v.ID)
	wantErr(t, "duplicate with the module off", err, views.ErrModuleDisabled)
	wantErr(t, "pin with the module off", e.svc.ReplacePins(ctx, vc, []views.PinInput{{ViewID: v.ID, GroupKey: "tickets"}}), views.ErrModuleDisabled)
	if e.runner.callCount() != before {
		t.Error("a query ran for a switched-off module")
	}
	if sb, _ := e.svc.Sidebar(ctx, vc); len(sb.Groups) != 0 {
		t.Errorf("sidebar shows a disabled module's view: %+v", sb)
	}
	// The owner can still see and archive it; it reports the module state.
	got, err := e.svc.Get(ctx, caller(owner), v.ID)
	if err != nil || got.ModuleEnabled {
		t.Errorf("get with module off: %v %+v", err, got)
	}
	if _, err := e.svc.Archive(ctx, caller(owner), v.ID, v.Version); err != nil {
		t.Errorf("archive with module off: %v", err)
	}
	e.gate.set(moduleTickets, true)
}

func TestResourcePermissionIsNeverGrantedByAShare(t *testing.T) {
	e := newEnv(t)
	owner, viewer := e.newUser(), e.newUser()
	tv, err := e.svc.Create(ctx, caller(owner, "tasks.work"), views.CreateInput{Resource: "tasks", Name: "my tasks", Definition: views.Definition{}})
	if err != nil {
		t.Fatal(err)
	}
	tv = e.share(owner, tv, []string{views.PermShare}, views.ShareInput{SubjectType: "user", SubjectID: viewer, Level: "edit"})
	// The viewer has no task permission: the shared view does not exist for them (not even its name).
	vc := caller(viewer)
	_, err = e.svc.Get(ctx, vc, tv.ID)
	wantErr(t, "get without resource permission", err, views.ErrNotFound)
	_, err = e.svc.Results(ctx, vc, tv.ID, views.ResultsInput{})
	wantErr(t, "results without resource permission", err, views.ErrNotFound)
	n := "x"
	_, err = e.svc.Update(ctx, vc, tv.ID, views.UpdateInput{ExpectedVersion: tv.Version, Name: &n})
	wantErr(t, "edit without resource permission", err, views.ErrNotFound)
	if list, _ := e.svc.List(ctx, vc, views.ListInput{}); len(list.Items) != 0 {
		t.Errorf("shared view of an unreadable resource listed: %+v", list)
	}
	if e.runner.callCount() != 0 {
		t.Error("a query ran")
	}
	// With the permission the viewer's own scope applies: the runner is called as the viewer.
	if _, err := e.svc.Results(ctx, caller(viewer, "tasks.work"), tv.ID, views.ResultsInput{}); err != nil {
		t.Fatalf("results with permission: %v", err)
	}
	e.runner.mu.Lock()
	got := e.runner.callers[len(e.runner.callers)-1].UserID
	e.runner.mu.Unlock()
	if got != viewer {
		t.Errorf("the query ran as %s, want the viewer", got)
	}
}

// ---------------------------------------------------------------- unreadable fields

func TestUnreadableFieldDegradesToNoRows(t *testing.T) {
	e := newEnv(t)
	owner := e.newUser()
	// status = open AND title contains X: the owner could use both when saving.
	def := views.Definition{Filter: filterJSON(t, `{"v":1,"root":{"type":"group","logic":"and","children":[
		{"type":"condition","field":"status","op":"equals","value":"open"},
		{"type":"condition","field":"title","op":"contains","value":"printer"}]},
		"sort":[{"field":"created_at","dir":"desc"}]}`)}
	v := e.create(owner, "v", def)
	oc := caller(owner)
	// The title field becomes unreadable: an AND condition on it can match nothing, so no query runs at all.
	info := defaultInfo()
	info.Fields = info.Fields[:1]
	e.runner.setInfo(info)
	before := e.runner.callCount()
	out, err := e.svc.Results(ctx, oc, v.ID, views.ResultsInput{})
	if err != nil || e.runner.callCount() != before {
		t.Fatalf("results: %v, calls %d->%d", err, before, e.runner.callCount())
	}
	if string(out.Items) != "[]" || len(out.Warnings) == 0 || out.Warnings[0].Code != query.CodeFieldUnavailable {
		t.Errorf("want no rows plus a warning, got %s %+v", out.Items, out.Warnings)
	}
	// In an OR group only that branch dies: the filter is narrowed, never widened.
	orDef := views.Definition{Filter: filterJSON(t, `{"v":1,"root":{"type":"group","logic":"or","children":[
		{"type":"condition","field":"status","op":"equals","value":"open"},
		{"type":"condition","field":"title","op":"contains","value":"printer"}]}}`)}
	e.runner.setInfo(defaultInfo())
	ov := e.create(owner, "or", orDef)
	e.runner.setInfo(info)
	out, err = e.svc.Results(ctx, oc, ov.ID, views.ResultsInput{})
	if err != nil || len(out.Warnings) != 1 {
		t.Fatalf("or results: %v %+v", err, out.Warnings)
	}
	sent := e.runner.lastCall().Filter
	if sent == nil || sent.Root == nil || len(sent.Root.Children) != 1 || sent.Root.Children[0].Field != "status" {
		t.Errorf("the unreadable branch was not removed from the OR group: %+v", sent)
	}
	// A restricted enum value counts as unreadable as well.
	info2 := defaultInfo()
	info2.Fields[0].EnumValues = []string{"closed"}
	e.runner.setInfo(info2)
	before = e.runner.callCount()
	out, _ = e.svc.Results(ctx, oc, v.ID, views.ResultsInput{})
	if e.runner.callCount() != before || string(out.Items) != "[]" {
		t.Errorf("a restricted enum value must evaluate to no rows: %s", out.Items)
	}
	// An unreadable sort key is dropped with a warning (a sort never widens a result).
	e.runner.setInfo(func() query.Info { i := defaultInfo(); i.Fields[2].Sortable = false; return i }())
	sv := e.create(owner, "sorted", views.Definition{Filter: filterJSON(t, `{"v":1,"sort":[{"field":"created_at","dir":"desc"}]}`)})
	out, err = e.svc.Results(ctx, oc, sv.ID, views.ResultsInput{})
	if err != nil || len(out.Warnings) != 1 || len(e.runner.lastCall().Filter.Sort) != 0 {
		t.Errorf("unreadable sort: %v %+v %+v", err, out.Warnings, e.runner.lastCall().Filter)
	}
}

func TestResultsPassThroughPagingAndCount(t *testing.T) {
	e := newEnv(t)
	owner := e.newUser()
	v := e.create(owner, "v", views.Definition{})
	n := 3
	e.runner.queryFn = func(_ views.Caller, _ string, req query.Request) (views.Result, error) {
		return views.Result{Items: json.RawMessage(`[1,2,3]`), NextCursor: "next-" + req.Cursor, Count: &n,
			Warnings: []query.Warning{{Code: "module.note", Path: "x"}}}, nil
	}
	out, err := e.svc.Results(ctx, caller(owner), v.ID, views.ResultsInput{Cursor: "c1", Limit: 5, Count: true})
	if err != nil || out.NextCursor != "next-c1" || out.Count == nil || *out.Count != 3 || out.View.Version != 1 || len(out.Warnings) != 1 {
		t.Fatalf("results: %v %+v", err, out)
	}
	if got := e.runner.lastCall(); got.Cursor != "c1" || got.Limit != 5 || !got.Count || got.Filter != nil {
		t.Errorf("request passed to the module: %+v", got)
	}
	// Module errors (rate limit, invalid cursor) reach the caller unchanged.
	e.runner.queryFn = func(views.Caller, string, query.Request) (views.Result, error) {
		return views.Result{}, &views.RunError{Status: 429, Code: "query.rate_limited", Message: "slow down"}
	}
	var re *views.RunError
	if _, err := e.svc.Results(ctx, caller(owner), v.ID, views.ResultsInput{}); !errors.As(err, &re) || re.Status != 429 {
		t.Errorf("rate limit not passed through: %v", err)
	}
}

// ---------------------------------------------------------------- audit hygiene

func TestAuditNeverContainsFilterValuesOrNames(t *testing.T) {
	e := newEnv(t)
	owner, viewer := e.newUser(), e.newUser()
	secret := "Mustermann-ACME-4711"
	def := views.Definition{Filter: filterJSON(t, fmt.Sprintf(`{"v":1,"root":{"type":"condition","field":"title","op":"contains","value":%q}}`, secret))}
	v := e.create(owner, "Personal name Erika Mustermann", def)
	n := "Renamed Erika"
	v2, err := e.svc.Update(ctx, caller(owner), v.ID, views.UpdateInput{ExpectedVersion: 1, Name: &n, Definition: &views.Definition{
		Filter: filterJSON(t, fmt.Sprintf(`{"v":1,"root":{"type":"condition","field":"title","op":"contains","value":"%s-2"}}`, secret))}})
	if err != nil {
		t.Fatal(err)
	}
	e.share(owner, v2, []string{views.PermShare}, views.ShareInput{SubjectType: "user", SubjectID: viewer, Level: "use"})
	rows, err := e.pool.Query(ctx, `SELECT action, COALESCE(metadata::text, '') || COALESCE(before_data::text, '') || COALESCE(after_data::text, '') FROM platform.audit_events WHERE target_type = 'view' AND target_id = $1`, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var action, payload string
		if err := rows.Scan(&action, &payload); err != nil {
			t.Fatal(err)
		}
		seen++
		for _, bad := range []string{secret, "Erika", "Mustermann"} {
			if strings.Contains(payload, bad) {
				t.Errorf("audit row %s leaks %q: %s", action, bad, payload)
			}
		}
	}
	if seen < 4 {
		t.Errorf("expected created, renamed, definition_changed and share rows, got %d", seen)
	}
}

func TestEventsForSharingAndArchiving(t *testing.T) {
	e := newEnv(t)
	owner, viewer := e.newUser(), e.newUser()
	v := e.create(owner, "v", views.Definition{})
	v = e.share(owner, v, []string{views.PermShare}, views.ShareInput{SubjectType: "user", SubjectID: viewer, Level: "use"})
	if _, err := e.svc.Archive(ctx, caller(owner), v.ID, v.Version); err != nil {
		t.Fatal(err)
	}
	for ev, want := range map[string]int{"ViewShared": 1, "ViewArchived": 1} {
		var n int
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM platform.outbox_events WHERE event_type = $1 AND payload->>'viewId' = $2`, ev, v.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Errorf("%s events = %d, want %d", ev, n, want)
		}
	}
}

// ---------------------------------------------------------------- cache key

func TestCacheKeyChangesWithEveryInput(t *testing.T) {
	base := views.CacheKey("v1", 3, "u1", []string{"a", "b"}, []string{"t1"}, []string{"r1"}, "queues:q1")
	if base != views.CacheKey("v1", 3, "u1", []string{"b", "a", "a"}, []string{"t1"}, []string{"r1"}, "queues:q1") {
		t.Error("the key must not depend on the order or repetition of set members")
	}
	variants := map[string]string{
		"view":        views.CacheKey("v2", 3, "u1", []string{"a", "b"}, []string{"t1"}, []string{"r1"}, "queues:q1"),
		"version":     views.CacheKey("v1", 4, "u1", []string{"a", "b"}, []string{"t1"}, []string{"r1"}, "queues:q1"),
		"principal":   views.CacheKey("v1", 3, "u2", []string{"a", "b"}, []string{"t1"}, []string{"r1"}, "queues:q1"),
		"permissions": views.CacheKey("v1", 3, "u1", []string{"a"}, []string{"t1"}, []string{"r1"}, "queues:q1"),
		"teams":       views.CacheKey("v1", 3, "u1", []string{"a", "b"}, []string{"t1", "t2"}, []string{"r1"}, "queues:q1"),
		"roles":       views.CacheKey("v1", 3, "u1", []string{"a", "b"}, []string{"t1"}, nil, "queues:q1"),
		"scope":       views.CacheKey("v1", 3, "u1", []string{"a", "b"}, []string{"t1"}, []string{"r1"}, "queues:q2"),
		"no scope":    views.CacheKey("v1", 3, "u1", []string{"a", "b"}, []string{"t1"}, []string{"r1"}),
		"boundaries":  views.CacheKey("v1", 3, "u1", []string{"ab"}, []string{"t1"}, []string{"r1"}, "queues:q1"),
	}
	seen := map[string]string{base: "base"}
	for name, k := range variants {
		if prev, dup := seen[k]; dup {
			t.Errorf("key for %s equals the key for %s", name, prev)
		}
		seen[k] = name
	}
	if views.Fingerprint("ab", "c") == views.Fingerprint("a", "bc") {
		t.Error("fingerprints must be length-prefixed")
	}
}
