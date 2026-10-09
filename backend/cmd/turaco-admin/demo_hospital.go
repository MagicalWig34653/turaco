package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	assetsapp "github.com/MagicalWig34653/turaco/backend/internal/modules/assets/application"
	briefingapp "github.com/MagicalWig34653/turaco/backend/internal/modules/briefing/application"
	briefingrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/briefing/repository"
	infraapp "github.com/MagicalWig34653/turaco/backend/internal/modules/infrastructure/application"
	knowledgeapp "github.com/MagicalWig34653/turaco/backend/internal/modules/knowledge/application"
	orgapp "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	orgrepo "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/repository"
	servicedeskapp "github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	tasksapp "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	tasksrepository "github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authentication"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization/roles"
	"github.com/MagicalWig34653/turaco/backend/internal/wiring"
)

const hospitalCorrelation = "demo-seed-hospital"

// hospitalSeeder creates the hospital simulation (docs/development/simulation-hospital.md). It builds on
// demoSeeder for products and catalog items. Everything goes through audited application operations (Locations,
// Departments, Teams, Roles and assignments use the People and role operations of F14) except the profile
// attributes of the simulation Users: they are emergency accounts (so the simulation can sign every persona in
// without a directory), whose lifecycle is CLI-only and which the People operations deliberately refuse, so
// their profile rows are written directly and audited as demo.hospital.* actions.
type hospitalSeeder struct {
	*demoSeeder

	users     map[string]string // login -> user id
	teams     map[string]string // team key -> team id
	locations map[string]string // site or area key -> location id
	depts     map[string]string // department key -> id
	roleIDs   map[string]string // role key -> id
	assets    map[string]string // serial -> asset id
	tickets   map[string]string // title -> ticket id
	products  map[string]string // internal part number -> product id

	counts map[string]int
}

func (h *hospitalSeeder) count(kind string) { h.counts[kind]++ }

func seedHospital(ctx context.Context, e env) error {
	h := &hospitalSeeder{
		demoSeeder: newDemoSeeder(e, hospitalCorrelation),
		users:      map[string]string{}, teams: map[string]string{}, locations: map[string]string{}, depts: map[string]string{},
		roleIDs: map[string]string{}, assets: map[string]string{}, tickets: map[string]string{}, products: map[string]string{},
		counts: map[string]int{},
	}
	steps := []struct {
		name string
		fn   func(context.Context) error
	}{
		{"organization", h.organization},
		{"teams", h.teamsAndMembers},
		{"users", h.people},
		{"roles", h.rolesAndAssignments},
		{"infrastructure", h.topology},
		{"products", h.productCatalog},
		{"assets", h.assetRegister},
		{"knowledge", h.knowledge},
		{"tickets", h.ticketsAndComments},
		{"problems", h.problems},
		{"major incident", h.majorIncident},
		{"tasks", h.workTasks},
		{"briefing", h.briefing},
		{"catalog", h.catalog},
	}
	for _, s := range steps {
		if err := s.fn(ctx); err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
	}
	keys := make([]string, 0, len(h.counts))
	for k := range h.counts {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		fmt.Fprintf(e.stdout, "created %d %s\n", h.counts[k], k)
	}
	fmt.Fprintf(e.stdout, "hospital simulation is ready: %d users, shared password %q (development only)\n", len(simPeople), hospitalPassword)
	return nil
}

func (h *hospitalSeeder) caller() orgapp.Caller {
	return orgapp.Caller{Actor: h.e.auditActor(), CorrelationID: hospitalCorrelation}
}

// ---- organization: locations, departments (People operations) ----

// seedRepo returns the Organization repository with the guards the People operations need; the seed runs as the
// CLI actor, which is exempt from the actor-relative rules.
func (h *hospitalSeeder) seedRepo() (*orgrepo.Repository, error) {
	return wiring.Organization(h.e.pool, wiring.OrganizationConfig{})
}

// findOrgRow returns the id of the active row of table with the code (or the legacy external key of earlier
// seeds). table is a constant of this file.
func (h *hospitalSeeder) findOrgRow(ctx context.Context, table, code string) (string, bool, error) {
	var id string
	err := h.e.pool.QueryRow(ctx, `SELECT id::text FROM organization.`+table+`
		WHERE lower(code) = lower($1) OR external_key = $1 ORDER BY created_at LIMIT 1`, code).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return id, err == nil, err
}

func (h *hospitalSeeder) organization(ctx context.Context) error {
	repo, err := h.seedRepo()
	if err != nil {
		return err
	}
	people := orgapp.NewPeople(repo)
	for _, s := range simSites {
		code := "SIM-SITE-" + s.Key
		id, found, err := h.findOrgRow(ctx, "locations", code)
		if err != nil {
			return fmt.Errorf("site %s: %w", s.Key, err)
		}
		if !found {
			l, err := people.CreateLocation(ctx, h.caller(), orgapp.NewLocationInput{Kind: orgapp.LocationSite, Name: s.Name, Code: &code})
			if err != nil {
				return fmt.Errorf("site %s: %w", s.Key, err)
			}
			id = l.ID
			h.count("locations")
		}
		h.locations[s.Key] = id
	}
	for _, a := range simAreas {
		code := "SIM-AREA-" + a.Key
		id, found, err := h.findOrgRow(ctx, "locations", code)
		if err != nil {
			return fmt.Errorf("area %s: %w", a.Key, err)
		}
		if !found {
			parent := h.locations[a.Site]
			l, err := people.CreateLocation(ctx, h.caller(), orgapp.NewLocationInput{Kind: orgapp.LocationArea, ParentID: &parent, Name: a.Name, Code: &code})
			if err != nil {
				return fmt.Errorf("area %s: %w", a.Key, err)
			}
			id = l.ID
			h.count("locations")
		}
		h.locations[a.Key] = id
	}
	for _, d := range simDepartments {
		code := "SIM-DEPT-" + d.Key
		id, found, err := h.findOrgRow(ctx, "departments", code)
		if err != nil {
			return fmt.Errorf("department %s: %w", d.Key, err)
		}
		if !found {
			dep, err := people.CreateDepartment(ctx, h.caller(), orgapp.NewDepartmentInput{Name: d.Name, Code: &code})
			if err != nil {
				return fmt.Errorf("department %s: %w", d.Key, err)
			}
			id = dep.ID
			h.count("departments")
		}
		h.depts[d.Key] = id
	}
	return nil
}

// ---- teams ----

func (h *hospitalSeeder) teamsAndMembers(ctx context.Context) error {
	reader := orgrepo.New(h.e.pool)
	teams := orgapp.NewTeams(reader)
	for _, t := range simTeams {
		created, err := teams.Create(ctx, h.caller(), t.Name)
		if errors.Is(err, orgapp.ErrConflict) {
			list, lerr := reader.ListTeams(ctx, orgapp.NameFilter{Query: t.Name, Page: orgapp.Page{Limit: 100}})
			if lerr != nil {
				return lerr
			}
			for _, x := range list.Items {
				if x.Name == t.Name && x.Active {
					created, err = x, nil
				}
			}
		} else if err == nil {
			h.count("teams")
		}
		if err != nil {
			return fmt.Errorf("team %q: %w", t.Name, err)
		}
		h.teams[t.Key] = created.ID
	}
	return nil
}

// ---- users ----

func (h *hospitalSeeder) people(ctx context.Context) error {
	accounts := h.e.emergencyAccounts()
	for _, p := range simPeople {
		cred, found, err := authentication.FindLocalCredential(ctx, h.e.pool, p.Login)
		if err != nil {
			return err
		}
		id := cred.UserID
		if !found {
			id, err = accounts.CreateAccount(ctx, h.e.auditActor(), p.Login, p.displayName(), hospitalPassword)
			if err != nil {
				return fmt.Errorf("create %s: %w", p.Login, err)
			}
			h.count("users")
		}
		if _, err := accounts.Enable(ctx, h.e.auditActor(), p.Login); err != nil {
			return fmt.Errorf("enable %s: %w", p.Login, err)
		}
		h.users[p.Login] = id
	}
	// Profile attributes need all user ids (managers), so they are written in a second pass.
	for _, p := range simPeople {
		if err := h.profile(ctx, p); err != nil {
			return fmt.Errorf("profile %s: %w", p.Login, err)
		}
	}
	teams := orgapp.NewTeams(orgrepo.New(h.e.pool))
	for _, p := range simPeople {
		for _, m := range p.Teams {
			// Leaders of the simulation (non-empty free-text title) are Team leads, everyone else a member.
			role := orgapp.TeamRoleMember
			if m.Role != "" {
				role = orgapp.TeamRoleLead
			}
			_, err := teams.AddMember(ctx, h.caller(), h.teams[m.Team], h.users[p.Login], &role)
			if errors.Is(err, orgapp.ErrConflict) {
				continue
			}
			if err != nil {
				return fmt.Errorf("add %s to team %s: %w", p.Login, m.Team, err)
			}
			h.count("team memberships")
		}
	}
	return nil
}

func nullable(m map[string]string, key string) *string {
	if v, ok := m[key]; ok && key != "" {
		return &v
	}
	return nil
}

func (h *hospitalSeeder) profile(ctx context.Context, p simPerson) error {
	return pgx.BeginFunc(ctx, h.e.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE organization.users
			SET given_name = $2::text, family_name = $3::text, primary_email = $4::text, department_id = $5::uuid,
			    primary_location_id = $6::uuid, manager_user_id = $7::uuid, updated_at = now()
			WHERE id = $1::uuid AND (given_name, family_name, primary_email, department_id, primary_location_id, manager_user_id)
			      IS DISTINCT FROM ($2::text, $3::text, $4::text, $5::uuid, $6::uuid, $7::uuid)`,
			h.users[p.Login], p.Given, p.Family, p.email(), nullable(h.depts, p.Dept), nullable(h.locations, p.Area), nullable(h.users, p.Manager))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		return audit.Record(ctx, tx, audit.Change{
			Action: "demo.hospital.user_profile_seeded", TargetType: "user", TargetID: h.users[p.Login], Actor: h.e.auditActor(),
			CorrelationID: hospitalCorrelation, After: map[string]any{"department": p.Dept, "location": p.Area, "manager": p.Manager},
		})
	})
}

// ---- roles ----

// simRoleTemplates maps the simulation role keys to the Role Template they start from. The roles keep their
// simulation-specific permission lists (explicit final lists recorded with template_key), so the seed also shows
// "template plus changes".
var simRoleTemplates = map[string]string{
	roleFirstLevel: "first-level-support", roleSpecialist: "it-specialist", roleSiteLead: "team-lead",
	roleSecurity: "security-analyst", roleInfra: "infrastructure-engineer", roleVendor: "vendor-restricted",
}

func (h *hospitalSeeder) rolesAndAssignments(ctx context.Context) error {
	svc := roles.NewService(h.e.pool, orgpublic.NewAuthorizationSubjects(orgrepo.New(h.e.pool)))
	actor := h.e.auditActor()
	// The simulation deliberately gives some roles combinations the separation-of-duties hygiene warns about
	// (the site lead approves the changes the team creates); the seed acknowledges them with a reason.
	var ackRules []string
	for _, r := range roles.SoDRules() {
		ackRules = append(ackRules, r.Key)
	}
	ack := roles.Acknowledgement{Rules: ackRules, Reason: "hospital simulation seed"}
	for _, r := range simRoles {
		role, err := svc.CreateRole(ctx, actor, hospitalCorrelation, roles.CreateRoleInput{Key: r.Key, Name: r.Name, Description: r.Description,
			Permissions: r.Permissions, TemplateKey: simRoleTemplates[r.Key], Acknowledgement: ack})
		if errors.Is(err, roles.ErrDuplicateKey) {
			role, err = svc.GetRoleByKey(ctx, r.Key)
			if err == nil && !samePermissions(role.Permissions, r.Permissions) {
				role, err = svc.SetRolePermissions(ctx, actor, hospitalCorrelation, role.ID,
					roles.SetRolePermissionsInput{Permissions: r.Permissions, ExpectedVersion: role.Version, Acknowledgement: ack})
			}
		} else if err == nil {
			h.count("roles")
		}
		if err != nil {
			return fmt.Errorf("role %s: %w", r.Key, err)
		}
		h.roleIDs[r.Key] = role.ID
	}
	for _, p := range simPeople {
		if p.Role == "" {
			continue
		}
		_, err := svc.AssignRole(ctx, actor, hospitalCorrelation, roles.AssignInput{RoleID: h.roleIDs[p.Role], SubjectType: roles.SubjectUser,
			SubjectID: h.users[p.Login], Acknowledgement: ack})
		if errors.Is(err, roles.ErrDuplicateAssignment) {
			continue
		}
		if err != nil {
			return fmt.Errorf("assign %s to %s: %w", p.Role, p.Login, err)
		}
		h.count("role assignments")
	}
	return nil
}

func samePermissions(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

// ---- infrastructure ----

func (h *hospitalSeeder) topology(ctx context.Context) error {
	svc := wiring.Infrastructure(h.e.pool)
	c := infraapp.Caller{Actor: h.e.auditActor(), CorrelationID: hospitalCorrelation}
	p := infraapp.Principal{Manage: true, View: true}
	page := infraapp.Page{Limit: 200}
	for _, b := range simBuildings {
		site := h.locations[b.Site]
		building, err := svc.CreateBuilding(ctx, c, p, site, b.Name, "")
		if errors.Is(err, infraapp.ErrConflict) {
			list, lerr := svc.ListBuildings(ctx, p, site, true, page)
			if lerr != nil {
				return lerr
			}
			for _, x := range list.Items {
				if strings.EqualFold(x.Name, b.Name) {
					building, err = x, nil
				}
			}
		} else if err == nil {
			h.count("buildings")
		}
		if err != nil {
			return fmt.Errorf("building %s: %w", b.Name, err)
		}
		for _, r := range b.Rooms {
			room, err := svc.CreateRoom(ctx, c, p, building.ID, r.Name, r.Floor)
			if errors.Is(err, infraapp.ErrConflict) {
				continue
			}
			if err != nil {
				return fmt.Errorf("room %s: %w", r.Name, err)
			}
			h.count("rooms")
			if r.Name == "Serverraum" || r.Name == "Rechenzentrum" {
				if _, err := svc.CreateRack(ctx, c, p, room.ID, "Rack 1", 42); err != nil && !errors.Is(err, infraapp.ErrConflict) {
					return fmt.Errorf("rack: %w", err)
				}
			}
		}
	}
	return nil
}

// ---- products ----

func (h *hospitalSeeder) productCatalog(ctx context.Context) error {
	cats := map[string]string{}
	vendors := map[string]string{}
	for _, p := range simProducts {
		if _, ok := cats[p.Category]; !ok {
			id, err := h.category(ctx, p.Category)
			if err != nil {
				return err
			}
			cats[p.Category] = id
		}
		if _, ok := vendors[p.Manufacturer]; !ok {
			id, err := h.manufacturer(ctx, p.Manufacturer)
			if err != nil {
				return err
			}
			vendors[p.Manufacturer] = id
		}
		id, err := h.product(ctx, vendors[p.Manufacturer], cats[p.Category], p.Name, p.MPN, p.IPN, true, true)
		if err != nil {
			return fmt.Errorf("product %s: %w", p.Name, err)
		}
		h.products[p.IPN] = id
	}
	return nil
}

// ---- assets ----

func (h *hospitalSeeder) assetRegister(ctx context.Context) error {
	svc := wiring.Assets(h.e.pool)
	c := assetsapp.Caller{Actor: h.e.auditActor(), CorrelationID: hospitalCorrelation}
	p := assetsapp.Principal{Manage: true, View: true}
	for _, a := range hospitalAssets() {
		loc := h.locations[a.Area]
		asset, err := svc.Create(ctx, c, p, assetsapp.CreateInput{
			ProductID: h.products[a.Part], SerialNumber: a.Serial, AssetTag: a.Tag, OwnershipType: "owned", LocationID: &loc,
		})
		switch {
		case errors.Is(err, assetsapp.ErrConflict):
			asset, err = svc.FindBySerial(ctx, a.Serial)
			if err != nil {
				return fmt.Errorf("asset %s: %w", a.Serial, err)
			}
		case err != nil:
			return fmt.Errorf("asset %s: %w", a.Serial, err)
		default:
			h.count("assets")
		}
		h.assets[a.Serial] = asset.ID
		if asset.Status != assetsapp.StatusAvailable {
			continue // already assigned or in repair by an earlier run
		}
		switch {
		case a.Repair != "":
			_, err = svc.Transition(ctx, c, p, asset.ID, nil, assetsapp.OpSendToRepair, assetsapp.Params{Reason: a.Repair})
		case a.Holder != "":
			var assignee assetsapp.Assignee
			if assignee, err = h.assignee(a.Holder); err == nil {
				_, err = svc.Transition(ctx, c, p, asset.ID, nil, assetsapp.OpAssign, assetsapp.Params{Assignee: assignee})
			}
		}
		if err != nil {
			return fmt.Errorf("asset %s: %w", a.Serial, err)
		}
	}
	return nil
}

func (h *hospitalSeeder) assignee(holder string) (assetsapp.Assignee, error) {
	kind, key, _ := strings.Cut(holder, ":")
	switch kind {
	case "user":
		return assetsapp.Assignee{Type: assetsapp.AssigneeUser, ID: h.users[key]}, nil
	case "team":
		return assetsapp.Assignee{Type: assetsapp.AssigneeTeam, ID: h.teams[key]}, nil
	case "location":
		return assetsapp.Assignee{Type: assetsapp.AssigneeLocation, ID: h.locations[key]}, nil
	}
	return assetsapp.Assignee{}, fmt.Errorf("unknown holder %q", holder)
}

// ---- knowledge ----

func (h *hospitalSeeder) knowledge(ctx context.Context) error {
	svc := wiring.Knowledge(h.e.pool)
	c := knowledgeapp.Caller{Actor: h.e.auditActor(), CorrelationID: hospitalCorrelation}
	p := knowledgeapp.Principal{UserID: "cli", Manage: true}
	existing, err := svc.List(ctx, p, "", "", knowledgeapp.Page{Limit: 200})
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, a := range existing.Items {
		have[a.Title] = true
	}
	for _, a := range simArticles {
		if have[a.Title] {
			continue
		}
		created, err := svc.Create(ctx, c, p, knowledgeapp.Input{Title: a.Title, Summary: a.Summary, Body: a.Body, Audience: a.Audience})
		if err != nil {
			return fmt.Errorf("article %q: %w", a.Title, err)
		}
		if _, err := svc.Publish(ctx, c, p, created.ID, nil); err != nil {
			return fmt.Errorf("publish %q: %w", a.Title, err)
		}
		h.count("articles")
	}
	return nil
}

// ---- tickets ----

func (h *hospitalSeeder) ticketsAndComments(ctx context.Context) error {
	svc := wiring.ServiceDesk(h.e.pool)
	c := servicedeskapp.Caller{Actor: h.e.auditActor(), CorrelationID: hospitalCorrelation}
	staff := servicedeskapp.Principal{UserID: "cli", View: true, Manage: true} // the CLI acts without a User; listing needs a non-empty id
	existing, err := h.allTickets(ctx, svc, staff)
	if err != nil {
		return err
	}
	for _, t := range simTickets {
		if id, ok := existing[t.Title]; ok {
			h.tickets[t.Title] = id
			continue
		}
		if err := h.ticket(ctx, svc, c, t); err != nil {
			return fmt.Errorf("ticket %q: %w", t.Title, err)
		}
		h.count("tickets")
	}
	return nil
}

func (h *hospitalSeeder) allTickets(ctx context.Context, svc *servicedeskapp.Service, staff servicedeskapp.Principal) (map[string]string, error) {
	out := map[string]string{}
	page := servicedeskapp.Page{Limit: servicedeskapp.MaxLimit}
	for {
		res, err := svc.List(ctx, staff, true, servicedeskapp.Filter{Page: page})
		if err != nil {
			return nil, err
		}
		for _, t := range res.Items {
			out[t.Title] = t.ID
		}
		if res.NextCursor == "" {
			return out, nil
		}
		page.Cursor = res.NextCursor
	}
}

func (h *hospitalSeeder) ticket(ctx context.Context, svc *servicedeskapp.Service, c servicedeskapp.Caller, t simTicket) error {
	reporter := servicedeskapp.Principal{UserID: h.users[t.Reporter], Manage: true}
	in := servicedeskapp.CreateInput{Title: t.Title, Description: t.Description, Priority: t.Priority}
	if t.Queue != "" {
		in.QueueTeamID = ptr(h.teams[t.Queue])
	}
	if t.Device {
		serial := h.deviceOf(t.Reporter)
		if serial == "" {
			return fmt.Errorf("%s holds no device", t.Reporter)
		}
		in.AssetID = ptr(h.assets[serial])
	}
	created, err := svc.Create(ctx, c, reporter, in)
	if err != nil {
		return err
	}
	h.tickets[t.Title] = created.ID
	if t.Assignee != "" {
		if _, err := svc.Assign(ctx, c, servicedeskapp.Principal{UserID: h.users[t.Assignee], Manage: true}, created.ID, nil, ptr(h.users[t.Assignee]), nil); err != nil {
			return fmt.Errorf("assign: %w", err)
		}
	}
	worker := servicedeskapp.Principal{UserID: h.users[t.Assignee], Manage: true}
	if t.Assignee == "" {
		worker = reporter
	}
	for _, c2 := range t.Comments {
		p := servicedeskapp.Principal{UserID: h.users[c2.By], Manage: true}
		if _, err := svc.AddComment(ctx, c, p, created.ID, c2.Body, c2.Internal); err != nil {
			return fmt.Errorf("comment: %w", err)
		}
	}
	for _, step := range t.Steps {
		op, text, _ := strings.Cut(step, ":")
		if _, err := svc.Transition(ctx, c, worker, created.ID, nil, op, servicedeskapp.Params{Reason: text}); err != nil {
			return fmt.Errorf("%s: %w", step, err)
		}
	}
	return nil
}

func ptr[T any](v T) *T { return &v }

// deviceOf returns the serial number of the first asset assigned to the user, or "".
func (h *hospitalSeeder) deviceOf(login string) string {
	for _, a := range hospitalAssets() {
		if a.Holder == "user:"+login {
			return a.Serial
		}
	}
	return ""
}

// ---- problems (known issues) ----

func (h *hospitalSeeder) problems(ctx context.Context) error {
	svc := wiring.Problems(h.e.pool)
	c := servicedeskapp.Caller{Actor: h.e.auditActor(), CorrelationID: hospitalCorrelation}
	p := servicedeskapp.ProblemPrincipal{Staff: true, Manage: true}
	have := map[string]bool{}
	page := servicedeskapp.Page{Limit: servicedeskapp.MaxLimit}
	for {
		res, err := svc.List(ctx, p, "", page)
		if err != nil {
			return err
		}
		for _, x := range res.Items {
			have[x.Title] = true
		}
		if res.NextCursor == "" {
			break
		}
		page.Cursor = res.NextCursor
	}
	for _, pr := range simProblems {
		if have[pr.Title] {
			continue
		}
		created, err := svc.CreateProblem(ctx, c, p, pr.Title, pr.Description)
		if err != nil {
			return fmt.Errorf("problem %q: %w", pr.Title, err)
		}
		if _, err := svc.SetOwner(ctx, c, p, created.ID, nil, h.users[pr.Owner]); err != nil {
			return fmt.Errorf("problem owner: %w", err)
		}
		for _, op := range pr.Ops {
			if _, err := svc.Transition(ctx, c, p, created.ID, nil, op.Op, servicedeskapp.ProblemParams{Text: op.Text}); err != nil {
				return fmt.Errorf("problem %q %s: %w", pr.Title, op.Op, err)
			}
		}
		for _, title := range pr.Tickets {
			if err := svc.LinkTicket(ctx, c, p, created.ID, h.tickets[title], true); err != nil && !errors.Is(err, servicedeskapp.ErrAlreadyLinked) {
				return fmt.Errorf("problem link %q: %w", title, err)
			}
		}
		h.count("problems")
	}
	return nil
}

// ---- major incident ----

func (h *hospitalSeeder) majorIncident(ctx context.Context) error {
	svc := wiring.MajorIncidents(h.e.pool)
	c := servicedeskapp.Caller{Actor: h.e.auditActor(), CorrelationID: hospitalCorrelation}
	list, err := svc.List(ctx, h.users["christian.hoffmann"], false, servicedeskapp.Page{Limit: servicedeskapp.MaxLimit})
	if err != nil {
		return err
	}
	for _, m := range list.Items {
		if m.Title == simMajorIncident.Title {
			return nil
		}
	}
	m, err := svc.Declare(ctx, c, true, simMajorIncident.Title, simMajorIncident.Summary)
	if err != nil {
		return err
	}
	for _, title := range simMajorIncident.Tickets {
		if err := svc.LinkTicket(ctx, c, true, m.ID, h.tickets[title]); err != nil && !errors.Is(err, servicedeskapp.ErrAlreadyLinked) {
			return fmt.Errorf("link %q: %w", title, err)
		}
	}
	h.count("major incidents")
	return nil
}

// ---- tasks ----

func (h *hospitalSeeder) workTasks(ctx context.Context) error {
	svc := tasksapp.NewService(tasksrepository.New(h.e.pool), orgpublic.NewWorkDirectory(orgrepo.New(h.e.pool)), nil)
	c := tasksapp.Caller{Actor: h.e.auditActor(), CorrelationID: hospitalCorrelation}
	p := tasksapp.Principal{Manage: true, ViewAll: true}
	for _, t := range simTasks {
		found, err := svc.List(ctx, p, tasksapp.ListFilter{TitlePrefix: t.Title, Page: tasksapp.Page{Limit: 5}})
		if err != nil {
			return err
		}
		if slices.ContainsFunc(found.Items, func(v tasksapp.TaskView) bool { return v.Title == t.Title }) {
			continue
		}
		due := time.Now().UTC().Add(time.Duration(t.DueInDays) * 24 * time.Hour)
		in := tasksapp.CreateInput{Title: t.Title, Description: t.Description, Priority: t.Priority, DueAt: &due, AssignedTeamID: ptr(h.teams[t.Team])}
		if t.User != "" {
			in.AssignedUserID = ptr(h.users[t.User])
		}
		if _, err := svc.Create(ctx, c, p, in); err != nil {
			return fmt.Errorf("task %q: %w", t.Title, err)
		}
		h.count("tasks")
	}
	return nil
}

// ---- briefing ----

func (h *hospitalSeeder) briefing(ctx context.Context) error {
	svc := briefingapp.NewService(briefingrepository.New(h.e.pool), nil)
	c := briefingapp.Caller{Actor: h.e.auditActor(), CorrelationID: hospitalCorrelation}
	p := briefingapp.Principal{UserID: "cli", Manage: true}
	existing, err := svc.List(ctx, p, "", briefingapp.Page{Limit: 200})
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, it := range existing.Items {
		have[it.Title] = true
	}
	for _, b := range simBriefings {
		if have[b.Title] {
			continue
		}
		until := time.Now().UTC().Add(time.Duration(b.ValidDays) * 24 * time.Hour)
		it, err := svc.Create(ctx, c, p, briefingapp.CreateInput{Title: b.Title, Body: b.Body, Severity: b.Severity, ValidUntil: &until})
		if err != nil {
			return fmt.Errorf("briefing %q: %w", b.Title, err)
		}
		if _, err := svc.Publish(ctx, c, p, it.ID, nil); err != nil {
			return fmt.Errorf("publish briefing %q: %w", b.Title, err)
		}
		h.count("briefing items")
	}
	return nil
}

// ---- catalog ----

func (h *hospitalSeeder) catalog(ctx context.Context) error {
	for _, it := range hospitalCatalogItems(h.teams) {
		created, err := h.item(ctx, it)
		if err != nil {
			return fmt.Errorf("catalog item %s: %w", it.Key, err)
		}
		if created {
			h.count("catalog items")
		}
	}
	return nil
}

// hospitalCatalogItems are the catalog items of the simulation. teams maps team keys to ids.
func hospitalCatalogItems(teams map[string]string) []demoItem {
	reason := map[string]any{"key": "reason", "type": "longtext", "label": "Begründung", "required": true, "maxLength": 1000}
	manager := []map[string]any{{"approver": "manager"}}
	team := func(key string) *string { return ptr(teams[key]) }
	return []demoItem{
		{
			Key: "orbis-access", Title: "ORBIS-Zugang beantragen", Description: "Zugriff auf ein ORBIS-Modul für sich oder eine Kollegin beantragen.",
			Definition: map[string]any{
				"allowRequestedFor": true,
				"fields": []map[string]any{
					{"key": "module", "type": "select", "label": "Modul", "required": true, "options": []map[string]any{
						{"value": "admission", "label": "Aufnahme"}, {"value": "medication", "label": "Medikation"},
						{"value": "orders", "label": "Leistungsanforderung"}, {"value": "findings", "label": "Befundung"}, {"value": "lab", "label": "Labor"},
					}},
					{"key": "level", "type": "select", "label": "Berechtigung", "required": true, "options": []map[string]any{
						{"value": "read", "label": "Lesen"}, {"value": "write", "label": "Lesen und Schreiben"},
					}},
					reason,
				},
				"approvals": manager,
				"fulfillment": []map[string]any{
					{"title": "ORBIS-Berechtigung einrichten", "priority": "high", "dueAfterHours": 24, "assignedTeamId": team(teamKIS)},
					{"title": "Berechtigung mit der Security prüfen", "priority": "normal", "mandatory": false, "assignedTeamId": team(teamSec)},
				},
			},
		},
		{
			Key: "medical-device-network", Title: "Medizingerät ins Netzwerk aufnehmen", Description: "Ein neues Medizin- oder IT-Gerät mit Netzwerk oder WLAN anbinden.",
			Definition: map[string]any{
				"fields": []map[string]any{
					{"key": "deviceType", "type": "select", "label": "Gerätetyp", "required": true, "options": []map[string]any{
						{"value": "terminal", "label": "Bedside-Terminal"}, {"value": "cart", "label": "Visitenwagen"},
						{"value": "modality", "label": "Bildgebung / Modalität"}, {"value": "other", "label": "Anderes Gerät"},
					}},
					{"key": "serial", "type": "text", "label": "Seriennummer", "required": true, "maxLength": 100},
					{"key": "location", "type": "text", "label": "Standort / Station", "required": true, "maxLength": 200},
					reason,
				},
				"approvals": []map[string]any{{"approverTeamId": team(teamSec)}},
				"fulfillment": []map[string]any{
					{"title": "Netzwerk-Port oder WLAN-Profil einrichten", "priority": "normal", "dueAfterHours": 72, "assignedTeamId": team(teamWLAN)},
					{"title": "Gerät im Inventar erfassen", "priority": "normal", "dueAfterHours": 72, "assignedTeamId": team(teamFLS)},
				},
			},
		},
		{
			Key: "dect-phone", Title: "DECT-Telefon anfordern", Description: "Ein Stations- oder Mobiltelefon beantragen.",
			Definition: map[string]any{
				"allowRequestedFor": true,
				"fields": []map[string]any{
					{"key": "station", "type": "text", "label": "Station / Abteilung", "required": true, "maxLength": 200},
					reason,
				},
				"approvals":   manager,
				"fulfillment": []map[string]any{{"title": "DECT-Telefon bereitstellen und Rufnummer zuweisen", "priority": "normal", "dueAfterHours": 72, "assignedTeamId": team(teamWLAN)}},
			},
		},
		{
			Key: "printer-setup", Title: "Drucker einrichten oder tauschen", Description: "Neuen Drucker einrichten oder defekten Drucker tauschen.",
			Definition: map[string]any{
				"fields": []map[string]any{
					{"key": "printer", "type": "text", "label": "Druckername oder Standort", "required": true, "maxLength": 200},
					{"key": "kind", "type": "select", "label": "Anliegen", "required": true, "options": []map[string]any{
						{"value": "new", "label": "Neu einrichten"}, {"value": "replace", "label": "Defekten Drucker tauschen"},
					}},
					reason,
				},
				"approvals":   []map[string]any{},
				"fulfillment": []map[string]any{{"title": "Drucker einrichten oder tauschen", "priority": "normal", "dueAfterHours": 48, "assignedTeamId": team(teamFLS)}},
			},
		},
	}
}
