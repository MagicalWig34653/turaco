package roles

import (
	"slices"
	"sort"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/permissions"
)

// Role Templates ship in the binary as plain data (A7 of the F14 design): a template is copied into an ordinary,
// editable role that records template_key and template_version and is never updated by a later release. They are
// not stored, so a release can add templates without a migration.
//
// Rules (a registry test enforces them): every name is a registered permission; no template contains a
// high-risk permission except the explicit opt-in "remote-support-attended" add-on, which is never combined into
// another template; platform.roles.manage, platform.admin, platform.audit.export and modules.manage are never in a
// template; no template violates a separation-of-duties rule.

// Template is one built-in Role Template.
type Template struct {
	Key     string
	Version int
	// Name and Description are the English fallbacks; the UI translates roles.template.<key>.name|description.
	Name        string
	Description string
	// Audience says who the template is for (documentation, shown in the picker).
	Audience    string
	Permissions []string
	// RequiresModules lists optional modules the template is only useful with.
	RequiresModules []string
	// ExternalOnly marks a template meant for external accounts only (the permission ceiling of external accounts).
	ExternalOnly bool
	// AdministratorAssignOnly marks a template whose role contains a high-risk permission: only a platform
	// administrator can create or assign it.
	AdministratorAssignOnly bool
}

var firstLevel = []string{"tickets.manage", "knowledge.view", "assets.view", "endpoints.view", "requests.view", "tasks.work", "remote_access.view"}

var specialist = concat(firstLevel, "assets.manage", "knowledge.manage", "problems.manage", "tasks.manage", "changes.view", "infrastructure.view", "security.view")

var templates = []Template{
	{Key: "first-level-support", Version: 1, Name: "First-level support", Audience: "Service desk triage",
		Description: "Triage and work tickets; read knowledge, assets, devices and requests; work assigned tasks. No attended remote access (add the Remote support template for that).",
		Permissions: firstLevel, RequiresModules: []string{"servicedesk"}},
	{Key: "it-specialist", Version: 1, Name: "IT specialist", Audience: "Second level (WLAN, hospital information system, servers)",
		Description: "First level plus asset and knowledge management, problems, task management, read access to changes, infrastructure and security.",
		Permissions: specialist, RequiresModules: []string{"servicedesk"}},
	{Key: "team-lead", Version: 1, Name: "Team or site lead", Audience: "Site IT lead",
		Description:     "IT specialist plus briefing, major incidents, requests, Team management, team availability and change management (not change approval).",
		Permissions:     concat(specialist, "briefing.manage", "majorincidents.manage", "requests.manage", "organization.teams.manage", "presence.view_availability", "changes.manage"),
		RequiresModules: []string{"servicedesk"}},
	{Key: "security-analyst", Version: 1, Name: "Security analyst", Audience: "Security",
		Description:     "Security advisories and findings, read access to tickets, devices, the directory and the audit log. Risk acceptance (security.accept_risk) is a separate, optional addition.",
		Permissions:     []string{"security.view", "security.manage", "tickets.view", "endpoints.view", "organization.directory.view", "platform.audit.view"},
		RequiresModules: []string{"security"}},
	{Key: "infrastructure-engineer", Version: 1, Name: "Infrastructure engineer", Audience: "Infrastructure",
		Description: "Buildings, rooms, racks, virtual machines, services and changes (including execution); reads tickets.",
		Permissions: []string{"infrastructure.view", "infrastructure.manage", "services.view", "services.manage", "assets.view", "assets.manage",
			"changes.view", "changes.manage", "changes.execute", "tickets.view"},
		RequiresModules: []string{"infrastructure", "changes"}},
	{Key: "vendor-restricted", Version: 1, Name: "Vendor (restricted)", Audience: "External vendor",
		Description: "Only tasks assigned to the vendor Team or to the person. Meant for external accounts.",
		Permissions: []string{"tasks.work"}, ExternalOnly: true},
	{Key: "employee-plus", Version: 1, Name: "Employee plus", Audience: "Everyone with extras",
		Description: "Tasks, knowledge and briefing. The implicit baseline of every signed-in user already covers raising and reading own tickets.",
		Permissions: []string{"tasks.work", "knowledge.view", "briefing.view"}},
	{Key: "remote-support-attended", Version: 1, Name: "Remote support (attended)", Audience: "Opt-in add-on for first level",
		Description: "Start attended remote-access sessions. This is a high-risk permission: only a platform administrator can create or assign this role, and it is never part of another template.",
		Permissions: []string{"remote_access.start_attended", "assets.view", "remote_access.view"}, RequiresModules: []string{"remoteaccess"},
		AdministratorAssignOnly: true},
}

func concat(base []string, more ...string) []string {
	out := slices.Clone(base)
	out = append(out, more...)
	sort.Strings(out)
	return slices.Compact(out)
}

// Templates returns the built-in Role Templates sorted by key (copies; callers may modify them).
func Templates() []Template {
	out := make([]Template, len(templates))
	for i, t := range templates {
		t.Permissions = slices.Clone(t.Permissions)
		sort.Strings(t.Permissions)
		t.RequiresModules = slices.Clone(t.RequiresModules)
		out[i] = t
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// TemplateByKey returns one template.
func TemplateByKey(key string) (Template, bool) {
	for _, t := range Templates() {
		if t.Key == key {
			return t, true
		}
	}
	return Template{}, false
}

// neverInTemplates are permissions no template may contain.
var neverInTemplates = []string{"platform.roles.manage", "platform.admin", "platform.audit.export", "modules.manage"}

// validateTemplates checks the registry rules; the registry test and nothing else calls it.
func validateTemplates() []string {
	var problems []string
	risk := registryRisk()
	seen := map[string]bool{}
	for _, t := range Templates() {
		if seen[t.Key] {
			problems = append(problems, "duplicate template key "+t.Key)
		}
		seen[t.Key] = true
		if !keyPattern.MatchString(t.Key) || t.Version < 1 {
			problems = append(problems, "invalid key or version of "+t.Key)
		}
		high := false
		for _, p := range t.Permissions {
			r, ok := risk[p]
			switch {
			case !ok:
				problems = append(problems, t.Key+" names unknown permission "+p)
			case r == RiskHigh:
				high = true
			}
			if slices.Contains(neverInTemplates, p) {
				problems = append(problems, t.Key+" contains forbidden permission "+p)
			}
		}
		if high != t.AdministratorAssignOnly {
			problems = append(problems, t.Key+": AdministratorAssignOnly must be set exactly when the template has a high-risk permission")
		}
		if high && t.Key != "remote-support-attended" {
			problems = append(problems, t.Key+" contains a high-risk permission")
		}
		set := map[string]struct{}{}
		for _, p := range t.Permissions {
			set[p] = struct{}{}
		}
		for _, v := range Violations(set) {
			problems = append(problems, t.Key+" violates separation-of-duties rule "+v.Key)
		}
		for _, p := range t.Permissions {
			for _, need := range neededBy(p) {
				if _, ok := set[need]; !ok {
					problems = append(problems, t.Key+": "+p+" needs "+need)
				}
			}
		}
	}
	return problems
}

// neededBy returns the companion permissions of a permission (registry data).
func neededBy(name string) []string {
	for _, p := range permissions.Registry {
		if p.Name == name {
			return p.Needs
		}
	}
	return nil
}

// MissingNeeds returns, for each permission of set that has companions, the companions that are not in set.
func MissingNeeds(set []string) map[string][]string {
	have := map[string]struct{}{}
	for _, p := range set {
		have[p] = struct{}{}
	}
	out := map[string][]string{}
	for _, p := range set {
		for _, need := range neededBy(p) {
			if _, ok := have[need]; !ok {
				out[p] = append(out[p], need)
			}
		}
	}
	return out
}
