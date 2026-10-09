package views_test

import (
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/views"
)

func TestPinsAndSidebar(t *testing.T) {
	e := newEnv(t)
	owner, viewer := e.newUser(), e.newUser()
	a := e.create(owner, "Alpha", views.Definition{})
	b := e.create(owner, "Beta", views.Definition{})
	oc := caller(owner)
	if err := e.svc.ReplacePins(ctx, oc, []views.PinInput{
		{ViewID: b.ID, GroupKey: "tickets", Position: 2},
		{ViewID: a.ID, GroupKey: "tickets", Position: 1},
	}); err != nil {
		t.Fatal(err)
	}
	sb, err := e.svc.Sidebar(ctx, oc)
	if err != nil || len(sb.Groups) != 1 || sb.Groups[0].Key != "tickets" || len(sb.Groups[0].Items) != 2 || sb.Groups[0].Items[0].ViewID != a.ID {
		t.Fatalf("sidebar order: %v %+v", err, sb)
	}
	got, _ := e.svc.Get(ctx, oc, a.ID)
	if !got.Pinned {
		t.Error("pinned flag missing")
	}
	// Hidden pins stay in the manage list, not in the sidebar.
	if err := e.svc.ReplacePins(ctx, oc, []views.PinInput{{ViewID: a.ID, GroupKey: "tickets", Hidden: true}}); err != nil {
		t.Fatal(err)
	}
	if sb, _ := e.svc.Sidebar(ctx, oc); len(sb.Groups) != 0 {
		t.Errorf("hidden pin in the sidebar: %+v", sb)
	}
	if pins, _ := e.svc.Pins(ctx, oc); len(pins) != 1 || !pins[0].Hidden || pins[0].Source != "user" {
		t.Errorf("manage list: %+v", pins)
	}
	// Validation.
	bad := map[string][]views.PinInput{
		"unknown group":    {{ViewID: a.ID, GroupKey: "nope"}},
		"duplicate":        {{ViewID: a.ID, GroupKey: "tickets"}, {ViewID: a.ID, GroupKey: "work"}},
		"negative":         {{ViewID: a.ID, GroupKey: "tickets", Position: -1}},
		"position too big": {{ViewID: a.ID, GroupKey: "tickets", Position: 10001}},
	}
	for name, in := range bad {
		wantInvalid(t, name, e.svc.ReplacePins(ctx, oc, in))
	}
	wantErr(t, "not a uuid", e.svc.ReplacePins(ctx, oc, []views.PinInput{{ViewID: "x", GroupKey: "tickets"}}), views.ErrNotFound)
	tooMany := make([]views.PinInput, views.MaxPinsPerUser+1)
	wantErr(t, "too many pins", e.svc.ReplacePins(ctx, oc, tooMany), views.ErrLimitReached)
	// A failed replacement changes nothing.
	if pins, _ := e.svc.Pins(ctx, oc); len(pins) != 1 {
		t.Errorf("a rejected replacement changed the pins: %+v", pins)
	}
	// Pinning somebody else's private view is refused as "not found", all or nothing.
	wantErr(t, "pin of a private view", e.svc.ReplacePins(ctx, caller(viewer), []views.PinInput{{ViewID: a.ID, GroupKey: "tickets"}}), views.ErrNotFound)
	// Collapsed groups.
	if err := e.svc.SetSidebarState(ctx, oc, []string{"tickets", "tickets", "tasks"}); err != nil {
		t.Fatal(err)
	}
	sb, _ = e.svc.Sidebar(ctx, oc)
	if len(sb.Collapsed) != 2 {
		t.Errorf("collapsed: %v", sb.Collapsed)
	}
	wantInvalid(t, "bad group key", e.svc.SetSidebarState(ctx, oc, []string{"Bad Key"}))
}

func TestPinRulePrivilegeBoundaries(t *testing.T) {
	e := newEnv(t)
	owner, member, publisher, sharer, admin, holder := e.newUser(), e.newUser(), e.newUser(), e.newUser(), e.newUser(), e.newUser()
	team := e.newTeam()
	e.dir.setTeams(member, team)
	v := e.create(owner, "Team board", statusFilter(t))
	rule := views.PinRuleInput{SubjectType: "team", SubjectID: team, GroupKey: "tickets"}

	// views.pin_for_groups is the ONE permission: neither publishing, sharing, administering nor owning the view
	// is enough to pin for a group.
	for name, perms := range map[string][]string{
		"no permission":           nil,
		"views.publish":           {views.PermPublish},
		"views.share":             {views.PermShare},
		"views.admin":             {views.PermAdmin},
		"all other views.* perms": {views.PermPublish, views.PermShare, views.PermAdmin},
	} {
		_, err := e.svc.CreatePinRule(ctx, caller(owner, perms...), v.ID, rule)
		wantErr(t, "create with "+name, err, views.ErrForbidden)
		_, err = e.svc.PinRules(ctx, caller(owner, perms...), v.ID)
		wantErr(t, "list with "+name, err, views.ErrForbidden)
		wantErr(t, "delete with "+name, e.svc.DeletePinRule(ctx, caller(owner, perms...), v.ID, e.newID()), views.ErrForbidden)
	}
	// The permission does not reach views the holder cannot use: no oracle about foreign private views.
	_, err := e.svc.CreatePinRule(ctx, caller(holder, views.PermPinForGroups), v.ID, rule)
	wantErr(t, "holder without access", err, views.ErrNotFound)
	_, err = e.svc.CreatePinRule(ctx, caller(holder, views.PermPinForGroups, views.PermAdmin), v.ID, rule)
	wantErr(t, "holder who is only an administrator", err, views.ErrNotFound)
	// Subjects must exist and be of the allowed kinds.
	oc := caller(owner, views.PermPinForGroups)
	_, err = e.svc.CreatePinRule(ctx, oc, v.ID, views.PinRuleInput{SubjectType: "team", SubjectID: e.newID(), GroupKey: "tickets"})
	wantErr(t, "unknown team", err, views.ErrSubjectNotFound)
	_, err = e.svc.CreatePinRule(ctx, oc, v.ID, views.PinRuleInput{SubjectType: "user", SubjectID: member, GroupKey: "tickets"})
	wantInvalid(t, "user subject", err)
	_, err = e.svc.CreatePinRule(ctx, oc, v.ID, views.PinRuleInput{SubjectType: "team", SubjectID: team, GroupKey: "evil"})
	wantInvalid(t, "unknown group", err)

	created, err := e.svc.CreatePinRule(ctx, oc, v.ID, rule)
	if err != nil || created.ViewID != v.ID {
		t.Fatalf("create rule: %v %+v", err, created)
	}
	// Creating the same rule again updates it (idempotent).
	again, err := e.svc.CreatePinRule(ctx, oc, v.ID, views.PinRuleInput{SubjectType: "team", SubjectID: team, GroupKey: "work", Position: 3})
	if err != nil || again.ID != created.ID || again.GroupKey != "work" {
		t.Errorf("upsert: %v %+v", err, again)
	}
	// A rule grants NOTHING: the member has no share, so the view does not exist for them.
	mc := caller(member)
	if sb, _ := e.svc.Sidebar(ctx, mc); len(sb.Groups) != 0 {
		t.Errorf("a pin rule revealed an unshared view: %+v", sb)
	}
	_, err = e.svc.Get(ctx, mc, v.ID)
	wantErr(t, "get through a rule", err, views.ErrNotFound)
	_, err = e.svc.Results(ctx, mc, v.ID, views.ResultsInput{})
	wantErr(t, "results through a rule", err, views.ErrNotFound)
	_ = publisher
	_ = sharer
	_ = admin

	// Sharing with the team makes the rule effective; revoking removes it again at once.
	v = e.share(owner, v, []string{views.PermShare}, views.ShareInput{SubjectType: "team", SubjectID: team, Level: "use"})
	sb, err := e.svc.Sidebar(ctx, mc)
	if err != nil || len(sb.Groups) != 1 || sb.Groups[0].Key != "work" || sb.Groups[0].Items[0].Source != "rule" {
		t.Fatalf("sidebar with rule and share: %v %+v", err, sb)
	}
	// The member hides it for themselves (override) and un-hides it again.
	if err := e.svc.ReplacePins(ctx, mc, []views.PinInput{{ViewID: v.ID, GroupKey: "work", Hidden: true}}); err != nil {
		t.Fatal(err)
	}
	if sb, _ := e.svc.Sidebar(ctx, mc); len(sb.Groups) != 0 {
		t.Errorf("hidden rule pin still shown: %+v", sb)
	}
	if err := e.svc.ReplacePins(ctx, mc, nil); err != nil {
		t.Fatal(err)
	}
	if sb, _ := e.svc.Sidebar(ctx, mc); len(sb.Groups) != 1 {
		t.Errorf("rule pin gone after dropping the override: %+v", sb)
	}
	// Someone who leaves the team loses the rule pin.
	e.dir.setTeams(member)
	if sb, _ := e.svc.Sidebar(ctx, mc); len(sb.Groups) != 0 {
		t.Errorf("rule pin after leaving the team: %+v", sb)
	}
	e.dir.setTeams(member, team)

	// Listing and deleting. A rule id of another view answers not found (no cross-view deletion).
	other := e.create(owner, "other view", views.Definition{})
	rules, err := e.svc.PinRules(ctx, oc, v.ID)
	if err != nil || len(rules) != 1 {
		t.Fatalf("rules: %v %+v", err, rules)
	}
	wantErr(t, "cross-view delete", e.svc.DeletePinRule(ctx, oc, other.ID, rules[0].ID), views.ErrNotFound)
	wantErr(t, "bad rule id", e.svc.DeletePinRule(ctx, oc, v.ID, "nope"), views.ErrNotFound)
	if err := e.svc.DeletePinRule(ctx, oc, v.ID, rules[0].ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if sb, _ := e.svc.Sidebar(ctx, mc); len(sb.Groups) != 0 {
		t.Errorf("rule pin after deleting the rule: %+v", sb)
	}
	if e.auditCount(v.ID, "views.pin_rule.created") != 2 || e.auditCount(v.ID, "views.pin_rule.deleted") != 1 {
		t.Error("pin rule changes not audited")
	}
	// An archived view cannot get a new rule.
	arch, _ := e.svc.Get(ctx, caller(owner), other.ID)
	if _, err := e.svc.Archive(ctx, caller(owner), other.ID, arch.Version); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.CreatePinRule(ctx, oc, other.ID, rule)
	if err == nil {
		t.Error("rule on an archived view accepted")
	}
}

func TestPinRuleForRolesAndLimit(t *testing.T) {
	e := newEnv(t)
	owner, holder := e.newUser(), e.newUser()
	role := e.newRole()
	e.assignRole(role, holder)
	v := e.create(owner, "v", views.Definition{})
	v = e.share(owner, v, []string{views.PermShare}, views.ShareInput{SubjectType: "role", SubjectID: role, Level: "use"})
	oc := caller(owner, views.PermPinForGroups)
	if _, err := e.svc.CreatePinRule(ctx, oc, v.ID, views.PinRuleInput{SubjectType: "role", SubjectID: role, GroupKey: "tickets"}); err != nil {
		t.Fatal(err)
	}
	if sb, _ := e.svc.Sidebar(ctx, caller(holder)); len(sb.Groups) != 1 {
		t.Errorf("role rule: %+v", sb)
	}
	for i := 1; i < views.MaxRulesPerView; i++ {
		if _, err := e.svc.CreatePinRule(ctx, oc, v.ID, views.PinRuleInput{SubjectType: "team", SubjectID: e.newTeam(), GroupKey: "tickets"}); err != nil {
			t.Fatalf("rule %d: %v", i, err)
		}
	}
	_, err := e.svc.CreatePinRule(ctx, oc, v.ID, views.PinRuleInput{SubjectType: "team", SubjectID: e.newTeam(), GroupKey: "tickets"})
	wantErr(t, "rule limit", err, views.ErrLimitReached)
}
