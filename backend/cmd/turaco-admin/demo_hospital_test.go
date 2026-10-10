package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	catalogapp "github.com/MagicalWig34653/turaco/backend/internal/modules/catalog/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authentication"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/permissions"
)

func TestHospitalSeedRefusesOutsideDevelopment(t *testing.T) {
	err := runDemo(context.Background(), env{cfg: config.Config{Environment: "production"}}, "seed-hospital", nil)
	if err == nil || !strings.Contains(err.Error(), "APP_ENV=development") {
		t.Errorf("err = %v", err)
	}
	if err := runDemo(context.Background(), env{}, "seed-hospital", []string{"x"}); err == nil {
		t.Error("arguments accepted")
	}
}

func keysOf[T any](items []T, key func(T) string) map[string]bool {
	out := map[string]bool{}
	for _, it := range items {
		out[key(it)] = true
	}
	return out
}

func TestHospitalPeople(t *testing.T) {
	if len(hospitalPassword) < authentication.MinPasswordLength {
		t.Fatalf("shared password has %d characters, policy needs %d", len(hospitalPassword), authentication.MinPasswordLength)
	}
	logins := map[string]bool{}
	emails := map[string]bool{}
	areas := keysOf(simAreas, func(a simArea) string { return a.Key })
	sites := keysOf(simSites, func(s simSite) string { return s.Key })
	depts := keysOf(simDepartments, func(d simDepartment) string { return d.Key })
	teams := keysOf(simTeams, func(x simTeam) string { return x.Key })
	roleKeys := keysOf(simRoles, func(r simRole) string { return r.Key })
	for _, p := range simPeople {
		if !authentication.ValidLoginName(p.Login) {
			t.Errorf("login %q is invalid", p.Login)
		}
		if logins[p.Login] || emails[p.email()] {
			t.Errorf("%s is not unique", p.Login)
		}
		logins[p.Login], emails[p.email()] = true, true
		if !sites[p.Site] || !depts[p.Dept] || (p.Area != "" && !areas[p.Area]) {
			t.Errorf("%s references an unknown site, department or area", p.Login)
		}
		if p.Role != "" && !roleKeys[p.Role] {
			t.Errorf("%s has unknown role %q", p.Login, p.Role)
		}
		for _, m := range p.Teams {
			if !teams[m.Team] {
				t.Errorf("%s is in unknown team %q", p.Login, m.Team)
			}
		}
	}
	for _, p := range simPeople {
		if p.Manager != "" && !logins[p.Manager] {
			t.Errorf("%s has unknown manager %q", p.Login, p.Manager)
		}
	}
	for _, a := range simAreas {
		if !sites[a.Site] {
			t.Errorf("area %s has unknown site", a.Key)
		}
	}
}

func TestHospitalTeamSizes(t *testing.T) {
	want := map[string]int{teamFLS: 2, teamWLAN: 3, teamKIS: 3, teamInfra: 2, teamSec: 2, teamLeads: 3, teamVendor: 2}
	got := map[string]int{}
	for _, p := range simPeople {
		for _, m := range p.Teams {
			got[m.Team]++
		}
	}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("team %s has %d members, want %d", k, got[k], n)
		}
	}
	staff := 0
	for _, p := range simPeople {
		if p.Role == "" {
			staff++
		}
	}
	if staff < 12 {
		t.Errorf("%d hospital staff users, want at least 12", staff)
	}
}

func TestHospitalRolesUseRegisteredPermissions(t *testing.T) {
	known := map[string]bool{}
	for _, p := range permissions.Registry {
		known[p.Name] = true
	}
	for _, r := range simRoles {
		if len(r.Permissions) == 0 {
			t.Errorf("role %s has no permissions", r.Key)
		}
		for _, p := range r.Permissions {
			if !known[p] {
				t.Errorf("role %s: unknown permission %q", r.Key, p)
			}
		}
	}
	// A vendor must never be able to work tickets or see assets.
	for _, p := range []string{"tickets.manage", "tickets.view", "assets.view", "knowledge.manage"} {
		for _, r := range simRoles {
			if r.Key == roleVendor && slices.Contains(r.Permissions, p) {
				t.Errorf("vendor role must not hold %s", p)
			}
		}
	}
}

func TestHospitalAssets(t *testing.T) {
	assets := hospitalAssets()
	if len(assets) != 69 {
		t.Fatalf("%d assets, want 69", len(assets))
	}
	products := keysOf(simProducts, func(p simProduct) string { return p.IPN })
	areas := keysOf(simAreas, func(a simArea) string { return a.Key })
	logins := keysOf(simPeople, func(p simPerson) string { return p.Login })
	teams := keysOf(simTeams, func(x simTeam) string { return x.Key })
	serials, tags := map[string]bool{}, map[string]bool{}
	for _, a := range assets {
		if serials[a.Serial] || tags[a.Tag] {
			t.Errorf("duplicate serial or tag %s/%s", a.Serial, a.Tag)
		}
		serials[a.Serial], tags[a.Tag] = true, true
		if !products[a.Part] || !areas[a.Area] {
			t.Errorf("%s references an unknown product or area", a.Serial)
		}
		kind, key, _ := strings.Cut(a.Holder, ":")
		switch kind {
		case "":
		case "user":
			if !logins[key] {
				t.Errorf("%s: unknown user %q", a.Serial, key)
			}
		case "team":
			if !teams[key] {
				t.Errorf("%s: unknown team %q", a.Serial, key)
			}
		case "location":
			if !areas[key] {
				t.Errorf("%s: unknown location %q", a.Serial, key)
			}
		default:
			t.Errorf("%s: unknown holder %q", a.Serial, a.Holder)
		}
	}
}

func TestHospitalTickets(t *testing.T) {
	if len(simTickets) < 25 {
		t.Errorf("%d tickets, want at least 25", len(simTickets))
	}
	logins := keysOf(simPeople, func(p simPerson) string { return p.Login })
	teams := keysOf(simTeams, func(x simTeam) string { return x.Key })
	ops := []string{"start", "wait", "resolve", "close", "cancel", "reopen"}
	titles := map[string]bool{}
	deviceHolders := map[string]bool{}
	for _, a := range hospitalAssets() {
		if kind, key, _ := strings.Cut(a.Holder, ":"); kind == "user" {
			deviceHolders[key] = true
		}
	}
	for _, tk := range simTickets {
		if titles[tk.Title] {
			t.Errorf("duplicate ticket title %q", tk.Title)
		}
		titles[tk.Title] = true
		if !logins[tk.Reporter] || (tk.Assignee != "" && !logins[tk.Assignee]) || (tk.Queue != "" && !teams[tk.Queue]) {
			t.Errorf("%q references an unknown person or team", tk.Title)
		}
		if tk.Device && !deviceHolders[tk.Reporter] {
			t.Errorf("%q: reporter holds no device", tk.Title)
		}
		if len(tk.Steps) > 0 && tk.Assignee == "" {
			t.Errorf("%q: lifecycle steps need an assignee", tk.Title)
		}
		for _, s := range tk.Steps {
			op, _, _ := strings.Cut(s, ":")
			if !slices.Contains(ops, op) {
				t.Errorf("%q: unknown step %q", tk.Title, s)
			}
		}
		for _, c := range tk.Comments {
			if !logins[c.By] {
				t.Errorf("%q: unknown commenter %q", tk.Title, c.By)
			}
		}
	}
	for _, pr := range simProblems {
		if !logins[pr.Owner] {
			t.Errorf("problem %q: unknown owner", pr.Title)
		}
		for _, title := range pr.Tickets {
			if !titles[title] {
				t.Errorf("problem %q links unknown ticket %q", pr.Title, title)
			}
		}
	}
	for _, title := range simMajorIncident.Tickets {
		if !titles[title] {
			t.Errorf("major incident links unknown ticket %q", title)
		}
	}
	internalRef := regexp.MustCompile(`\b[A-Z]{3,4}-\d+`)
	for _, task := range simTasks {
		if task.Team == teamVendor && internalRef.MatchString(task.Title+" "+task.Description) {
			t.Errorf("vendor task %q mentions an internal reference", task.Title)
		}
		if !teams[task.Team] || (task.User != "" && !logins[task.User]) {
			t.Errorf("task %q references an unknown team or person", task.Title)
		}
	}
}

func TestHospitalCatalogItemsAreValidDefinitions(t *testing.T) {
	const id = "0194f0a0-0000-7000-8000-000000000001"
	ids := map[string]string{}
	for _, tm := range simTeams {
		ids[tm.Key] = id
	}
	items := hospitalCatalogItems(ids)
	if len(items) != len(simCatalogKeys) {
		t.Fatalf("%d items, want %d", len(items), len(simCatalogKeys))
	}
	for i, it := range items {
		if it.Key != simCatalogKeys[i] {
			t.Errorf("item %d is %s, want %s", i, it.Key, simCatalogKeys[i])
		}
		raw, err := json.Marshal(it.Definition)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := catalogapp.ParseDefinition(raw); err != nil {
			t.Errorf("%s: %v", it.Key, err)
		}
	}
}

// TestHospitalDocumentationListsEveryLogin keeps the persona guide in sync with the seed data.
func TestHospitalDocumentationListsEveryLogin(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "development", "simulation-hospital.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	for _, p := range simPeople {
		if !strings.Contains(doc, "`"+p.Login+"`") {
			t.Errorf("simulation-hospital.md does not list login %s", p.Login)
		}
	}
	if !strings.Contains(doc, hospitalPassword) {
		t.Error("simulation-hospital.md does not state the shared password")
	}
	for _, r := range simRoles {
		if !strings.Contains(doc, "`"+r.Key+"`") {
			t.Errorf("simulation-hospital.md does not describe role %s", r.Key)
		}
	}
}

func TestHospitalQueues(t *testing.T) {
	teams := keysOf(simTeams, func(x simTeam) string { return x.Key })
	keys, prefixes := map[string]bool{"it": true}, map[string]bool{"TKT": true}
	for _, q := range simQueues {
		if !teams[q.Team] || keys[q.Key] || prefixes[q.Prefix] {
			t.Errorf("queue %s: unknown team or duplicate key/prefix", q.Key)
		}
		keys[q.Key], prefixes[q.Prefix] = true, true
	}
	// Tickets of the specialist Teams are routed into their desk; the others stay in the intake desk.
	routed := 0
	for _, tk := range simTickets {
		if _, ok := queueOfTeam(tk.Queue); ok {
			routed++
		}
	}
	if routed < 10 {
		t.Errorf("only %d tickets are routed into a specialist desk", routed)
	}
	if _, ok := queueOfTeam(teamFLS); ok {
		t.Error("First Level Support works the intake desk, not a specialist desk")
	}
}
