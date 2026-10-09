package roles

import "sort"

// Separation-of-duties rules (F14 section 2.4) are advisory hygiene, not enforcement: the runtime checks of the
// modules (never approve what you registered) remain. A rule is violated when a permission set contains at least
// one permission of Left and at least one of Right. A violation does not block; the caller acknowledges the rule
// keys with a reason (Acknowledgement), which is audited as authorization.role.sod_acknowledged.

// SoDRule is one rule. The UI translates roles.sod.<Key>.
type SoDRule struct {
	Key   string
	Left  []string
	Right []string
}

var operationsManage = []string{"tickets.manage", "tasks.manage", "assets.manage", "changes.manage", "infrastructure.manage",
	"services.manage", "requests.manage", "procurement.manage", "inventory.manage", "endpoints.manage"}

var sodRules = []SoDRule{
	{Key: "changes_manage_approve", Left: []string{"changes.manage"}, Right: []string{"changes.approve"}},
	{Key: "software_package_approve", Left: []string{"software.package"}, Right: []string{"software.approve"}},
	{Key: "deployments_manage_approve", Left: []string{"deployments.manage", "deployments.high_impact"}, Right: []string{"deployments.approve"}},
	{Key: "security_manage_accept_risk", Left: []string{"security.manage"}, Right: []string{"security.accept_risk"}},
	{Key: "procurement_inventory", Left: []string{"procurement.manage"}, Right: []string{"inventory.manage"}},
	{Key: "roles_manage_operations", Left: []string{"platform.roles.manage"}, Right: operationsManage},
	{Key: "audit_view_roles_manage", Left: []string{"platform.audit.view"}, Right: []string{"platform.roles.manage"}},
}

// SoDRules returns the rules sorted by key.
func SoDRules() []SoDRule {
	out := make([]SoDRule, len(sodRules))
	copy(out, sodRules)
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Violations returns the rules a permission set violates, sorted by key.
func Violations(set map[string]struct{}) []SoDRule {
	has := func(names []string) bool {
		for _, n := range names {
			if _, ok := set[n]; ok {
				return true
			}
		}
		return false
	}
	var out []SoDRule
	for _, r := range SoDRules() {
		if has(r.Left) && has(r.Right) {
			out = append(out, r)
		}
	}
	return out
}

func permSet(perms []string) map[string]struct{} {
	m := make(map[string]struct{}, len(perms))
	for _, p := range perms {
		m[p] = struct{}{}
	}
	return m
}
