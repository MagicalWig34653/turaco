package roles

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/permissions"
)

// Evaluator evaluates the effective permissions of a User per request
// (no cache): active, unexpired role assignments of the User and of the User's
// Directory Groups (transitive, via GroupResolver). The built-in administrator
// role yields every registered permission; other roles their stored
// permissions intersected with the registry. A User with a local credential
// never holds a high-risk permission (ADR-0034, review rule R10: until a
// step-up ADR exists the evaluator is the second line behind the assignment
// guard). It satisfies authentication.PermissionLoader.
//
// Location scope (ADR-0040): a global assignment yields the global set (Permissions, Principal.Has). A
// Location-scoped assignment yields only its scopable permissions, as the scoped map permission -> Locations,
// expanded per request to the current subtrees through LocationTree; its global-only permissions are inert. Every
// read that existed before scope (Permissions, PermissionsIn, RoleIDs) answers from global assignments only, so code
// that has not adopted scope never sees a scoped right. The guards' holdings carry both and compare per Location.
//
// Permissions, Access and Explain share one query (load) and one reduction (reduce),
// so the explanation can never disagree with the decision.
type Evaluator struct {
	pool   *pgxpool.Pool
	groups GroupResolver
	// scopable is permissions.Scopable; package tests replace it because no permission is scopable yet (slice L-A).
	scopable func(string) bool
}

// NewEvaluator creates the evaluator.
func NewEvaluator(pool *pgxpool.Pool, groups GroupResolver) *Evaluator {
	return &Evaluator{pool: pool, groups: groups, scopable: permissions.Scopable}
}

// grant is one active assignment path to a role.
type grant struct {
	roleID       string
	roleKey      string
	roleName     string
	builtIn      bool
	perms        []string
	assignmentID string
	subjectType  string
	subjectID    string
	expiresAt    *time.Time
	// scope is ScopeGlobal or ScopeLocation; scopeLocationID is set for ScopeLocation only.
	scope           string
	scopeLocationID string
}

func (g grant) global() bool { return g.scope == ScopeGlobal }

// grantsSQL selects the unexpired assignments (global and Location-scoped) that apply to $1 (a User) or $2 (the
// User's Directory Groups) with their live roles.
const grantsSQL = `
	SELECT r.id::text, r.key, r.name, r.built_in,
	       COALESCE((SELECT array_agg(rp.permission) FROM platform.role_permissions rp WHERE rp.role_id = r.id), '{}'::text[]),
	       a.id::text, a.subject_type, a.subject_id::text, a.expires_at, a.scope, COALESCE(a.scope_location_id::text, ''),
	       EXISTS (SELECT 1 FROM platform.local_credentials c WHERE c.user_id = $1::uuid AND c.kind = 'local')
	FROM platform.role_assignments a
	JOIN platform.roles r ON r.id = a.role_id AND r.deleted_at IS NULL
	WHERE a.revoked_at IS NULL AND (a.expires_at IS NULL OR a.expires_at > now()) AND (
		(a.subject_type = 'user' AND a.subject_id = $1::uuid)
		OR (a.subject_type = 'directory_group' AND a.subject_id = ANY($2::text[]::uuid[])))
	ORDER BY a.id`

func (e *Evaluator) load(ctx context.Context, q querier, userID string) (grants []grant, local, external bool, err error) {
	if !uuidPattern.MatchString(userID) {
		return nil, false, false, nil
	}
	groupIDs, err := e.groups.GroupIDsOfUser(ctx, userID)
	if err != nil {
		return nil, false, false, fmt.Errorf("resolve groups: %w", err)
	}
	if external, err = e.groups.IsExternalAccount(ctx, userID); err != nil {
		return nil, false, false, fmt.Errorf("resolve account kind: %w", err)
	}
	if groupIDs == nil {
		groupIDs = []string{}
	}
	rows, err := q.Query(ctx, grantsSQL, userID, groupIDs)
	if err != nil {
		return nil, false, false, fmt.Errorf("load role permissions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var g grant
		if err := rows.Scan(&g.roleID, &g.roleKey, &g.roleName, &g.builtIn, &g.perms, &g.assignmentID, &g.subjectType, &g.subjectID,
			&g.expiresAt, &g.scope, &g.scopeLocationID, &local); err != nil {
			return nil, false, false, fmt.Errorf("load role permissions: scan: %w", err)
		}
		grants = append(grants, g)
	}
	if err := rows.Err(); err != nil {
		return nil, false, false, fmt.Errorf("load role permissions: %w", err)
	}
	return grants, local, external, nil
}

func registryRisk() map[string]string {
	out := make(map[string]string, len(permissions.Registry))
	for _, p := range permissions.Registry {
		out[p.Name] = p.Risk
	}
	return out
}

// grantPermissions are the registered permissions a grant yields.
func grantPermissions(g grant, known map[string]string) []string {
	if g.builtIn {
		out := make([]string, 0, len(known))
		for p := range known {
			out = append(out, p)
		}
		sort.Strings(out)
		return out
	}
	out := make([]string, 0, len(g.perms))
	for _, p := range g.perms {
		if _, ok := known[p]; ok {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// reduction is the outcome of reduce.
type reduction struct {
	// global is the set held through global assignments: exactly the pre-scope effective set.
	global map[string]struct{}
	// scoped maps a scopable permission that is not in global to the sorted, distinct root Location ids of the
	// scoped assignments that grant it.
	scoped map[string][]string
	// scopedBy maps permission -> root Location -> granting assignment ids (for Explain).
	scopedBy map[string]map[string][]string
	// inert maps a scoped assignment id to the global-only permissions it reaches that are not held globally.
	inert map[string][]string
	// excluded are the permissions the local-account rule or the external ceiling removed.
	excluded []string
}

// reduce is the one place that turns grants into the effective sets. excluded lists the high-risk permissions the
// local-account rule or the external ceiling removed (for an external account the built-in administrator role
// yields everything and the ceiling removes all but the ceiling, which is not listed one by one). The exclusions run
// before the scope split, so a scope never widens anything. A scoped grant of the built-in administrator role (which
// the database and the operations refuse) and any scoped grant of an external account yield nothing but inert
// entries (fail closed).
func reduce(grants []grant, local, external bool, scopable func(string) bool) reduction {
	known := registryRisk()
	out := reduction{global: map[string]struct{}{}, scoped: map[string][]string{}, scopedBy: map[string]map[string][]string{}, inert: map[string][]string{}}
	removed := map[string]struct{}{}
	for _, g := range grants {
		for _, p := range grantPermissions(g, known) {
			if local && known[p] == RiskHigh {
				removed[p] = struct{}{}
				continue
			}
			if external && !InExternalCeiling(p) {
				if !g.builtIn {
					removed[p] = struct{}{}
				}
				continue
			}
			switch {
			case g.global():
				out.global[p] = struct{}{}
			case g.scope == ScopeLocation && g.scopeLocationID != "" && !g.builtIn && !external && scopable(p):
				if out.scopedBy[p] == nil {
					out.scopedBy[p] = map[string][]string{}
				}
				out.scopedBy[p][g.scopeLocationID] = append(out.scopedBy[p][g.scopeLocationID], g.assignmentID)
			default:
				out.inert[g.assignmentID] = append(out.inert[g.assignmentID], p)
			}
		}
	}
	for p, roots := range out.scopedBy {
		if _, held := out.global[p]; held {
			delete(out.scopedBy, p)
			continue
		}
		ids := make([]string, 0, len(roots))
		for root := range roots {
			ids = append(ids, root)
		}
		sort.Strings(ids)
		out.scoped[p] = ids
	}
	for id, perms := range out.inert {
		kept := perms[:0]
		for _, p := range perms {
			if _, held := out.global[p]; !held {
				kept = append(kept, p)
			}
		}
		if len(kept) == 0 {
			delete(out.inert, id)
			continue
		}
		out.inert[id] = slices.Compact(kept)
	}
	for p := range removed {
		_, inGlobal := out.global[p]
		_, inScoped := out.scoped[p]
		if !inGlobal && !inScoped {
			out.excluded = append(out.excluded, p)
		}
	}
	sort.Strings(out.excluded)
	return out
}

// MaxScopedRoots caps the distinct scoped root Locations of one permission (or role) of one User; the
// organization/public tree contract accepts at most this many roots per call. Roots beyond the cap (in id order)
// are dropped and logged.
const MaxScopedRoots = 1000

// expand turns the scoped map (permission -> root Locations) into permission -> expanded Location set. Permissions
// with the same roots share one LocationTree call (a User with one scoped role at one Site needs one call). A root
// that is not a Location contributes nothing; a permission whose roots expand to nothing is left out.
func (e *Evaluator) expand(ctx context.Context, scoped map[string][]string) (map[string]authorization.LocationScope, error) {
	out := map[string]authorization.LocationScope{}
	if len(scoped) == 0 {
		return out, nil
	}
	byRoots := map[string][]string{}
	for name, roots := range scoped {
		if len(roots) > MaxScopedRoots {
			// Far beyond any real installation; keep the request working with fewer Locations (fail closed) instead
			// of failing the whole session, and tell the operator.
			slog.WarnContext(ctx, "location scope: scoped roots capped", "entry", name, "roots", len(roots), "cap", MaxScopedRoots)
			roots = roots[:MaxScopedRoots]
			scoped[name] = roots
		}
		key := strings.Join(roots, ",")
		if _, done := byRoots[key]; done {
			continue
		}
		ids, err := e.groups.SubtreeIDs(ctx, roots)
		if err != nil {
			return nil, fmt.Errorf("expand location scope: %w", err)
		}
		sort.Strings(ids)
		byRoots[key] = slices.Compact(ids)
	}
	for p, roots := range scoped {
		ids := byRoots[strings.Join(roots, ",")]
		if len(ids) == 0 {
			continue
		}
		out[p] = authorization.LocationScope{Locations: slices.Clone(ids)}
	}
	return out, nil
}

// ExternalCeiling is the closed list of permissions an external account can ever hold (ADR-0034 point 4). The
// evaluator intersects the permissions of an external account with it, whatever roles are assigned. knowledge.view
// joins the list when knowledge articles gain an external audience; until then no knowledge permission is in it
// (the baseline "published employee articles" is closed to external accounts by the HTTP allow-list).
var ExternalCeiling = []string{"tasks.work"}

// InExternalCeiling reports whether an external account may hold the permission.
func InExternalCeiling(permission string) bool { return slices.Contains(ExternalCeiling, permission) }

// RiskHigh is the registry risk class of permissions that only a platform administrator may hand out.
const RiskHigh = "high"

// Permissions returns the permissions userID holds globally (Principal.Permissions). Scoped holdings are not part
// of it; see Access.
func (e *Evaluator) Permissions(ctx context.Context, userID string) (map[string]struct{}, error) {
	return e.PermissionsIn(ctx, e.pool, userID)
}

// PermissionsIn is Permissions inside the caller's transaction (guards evaluate the actor and the target in the
// transaction that changes them).
func (e *Evaluator) PermissionsIn(ctx context.Context, q querier, userID string) (map[string]struct{}, error) {
	grants, local, external, err := e.load(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	return reduce(grants, local, external, e.scopable).global, nil
}

// Access returns the global set and the expanded scoped map of userID (Principal.Permissions and the Principal's scoped holdings, WithScopes).
// The Location tree is read only when the User has scoped holdings of a scopable permission.
// ScopesOf returns where the User holds the two permissions through Location-scoped assignments (the expanded
// Location sets, empty when held globally or not at all). Callers outside the authorization code read scoped
// holdings only through this method or Principal.ScopeOf.
func (e *Evaluator) ScopesOf(ctx context.Context, userID, first, second string) (authorization.LocationScope, authorization.LocationScope, error) {
	acc, err := e.Access(ctx, userID)
	if err != nil {
		return authorization.LocationScope{}, authorization.LocationScope{}, err
	}
	return acc.Scopes[first], acc.Scopes[second], nil
}

func (e *Evaluator) Access(ctx context.Context, userID string) (authorization.Access, error) {
	grants, local, external, err := e.load(ctx, e.pool, userID)
	if err != nil {
		return authorization.Access{}, err
	}
	r := reduce(grants, local, external, e.scopable)
	scopes, err := e.expand(ctx, r.scoped)
	if err != nil {
		return authorization.Access{}, err
	}
	return authorization.Access{Permissions: r.global, Scopes: scopes}, nil
}

// RoleIDs returns the ids of the roles that apply to the User through global assignments (to the User or to one of
// the User's Directory Groups). Modules use them to resolve grants that name a role. A role held only at Locations
// is left out, so a module that has not adopted scope never turns a scoped role into a whole-Queue grant; such
// modules move to RoleGrants when they adopt scope.
func (e *Evaluator) RoleIDs(ctx context.Context, userID string) ([]string, error) {
	grants, _, _, err := e.load(ctx, e.pool, userID)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	out := []string{}
	for _, g := range grants {
		if !g.global() {
			continue
		}
		if _, dup := seen[g.roleID]; dup {
			continue
		}
		seen[g.roleID] = struct{}{}
		out = append(out, g.roleID)
	}
	return out, nil
}

// RoleScope is where a role applies to a User: everywhere (Global) or at the expanded Locations.
type RoleScope struct {
	RoleID    string
	Global    bool
	Locations []string
}

// RoleGrants returns the roles that apply to the User with their scope (F16 design L4, L6): Global when any global
// assignment reaches the role, otherwise the union of the expanded subtrees of its scoped assignments. Scoped
// assignments of an external account and of the built-in administrator role are left out (fail closed), as is a
// role whose scoped Locations all no longer exist. Ordered by role id.
func (e *Evaluator) RoleGrants(ctx context.Context, userID string) ([]RoleScope, error) {
	grants, _, external, err := e.load(ctx, e.pool, userID)
	if err != nil {
		return nil, err
	}
	global := map[string]bool{}
	roots := map[string]map[string]struct{}{}
	for _, g := range grants {
		switch {
		case g.global():
			global[g.roleID] = true
		case g.scope == ScopeLocation && g.scopeLocationID != "" && !g.builtIn && !external:
			if roots[g.roleID] == nil {
				roots[g.roleID] = map[string]struct{}{}
			}
			roots[g.roleID][g.scopeLocationID] = struct{}{}
		}
	}
	scoped := map[string][]string{}
	for roleID, set := range roots {
		if global[roleID] {
			continue
		}
		ids := make([]string, 0, len(set))
		for id := range set {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		scoped[roleID] = ids
	}
	expanded, err := e.expand(ctx, scoped)
	if err != nil {
		return nil, err
	}
	out := []RoleScope{}
	for roleID := range global {
		out = append(out, RoleScope{RoleID: roleID, Global: true})
	}
	for roleID, s := range expanded {
		out = append(out, RoleScope{RoleID: roleID, Locations: s.Locations})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RoleID < out[j].RoleID })
	return out, nil
}

// RoleGrant is one way a role reaches a User.
type RoleGrant struct {
	RoleID       string
	RoleKey      string
	RoleName     string
	BuiltInAdmin bool
	AssignmentID string
	// Source is "direct" or "directory_group".
	Source    string
	GroupID   string
	ExpiresAt *time.Time
	// Scope is ScopeGlobal or ScopeLocation; ScopeLocationID is the scoped root Location.
	Scope           string
	ScopeLocationID string
}

// ExplainedPermission is one permission held globally with the assignments that grant it.
type ExplainedPermission struct {
	Name string
	Risk string
	// GrantedBy are assignment ids into Explanation.Roles.
	GrantedBy []string
}

// ScopedRoot is one scoped root Location of a permission with the assignments that grant it there.
type ScopedRoot struct {
	RootLocationID string
	GrantedBy      []string
}

// ScopedPermission is a scopable permission held only at Locations.
type ScopedPermission struct {
	Name  string
	Risk  string
	Roots []ScopedRoot
	// Locations is the expanded set (the roots and their current descendants); it equals Access().Scopes[Name].
	Locations []string
}

// InertGrant lists the global-only permissions a scoped assignment reaches without effect.
type InertGrant struct {
	AssignmentID string
	Permissions  []string
}

// Explanation is the effective permission set with its grant paths.
type Explanation struct {
	Roles []RoleGrant
	// Permissions are held globally (scope global); they equal Permissions(userID).
	Permissions []ExplainedPermission
	// Scoped are the permissions held only at Locations; they equal Access(userID).Scopes.
	Scoped []ScopedPermission
	// Inert are the global-only permissions scoped assignments reach without effect.
	Inert []InertGrant
	// LocalAccount is true for a User with a local credential; Excluded lists the high-risk permissions its roles
	// would grant and the rule removed.
	LocalAccount bool
	// External is true for an external account; Excluded then also lists the permissions of its roles that lie
	// outside the external ceiling.
	External bool
	Excluded []string
}

// Explain returns the effective permissions of userID with their grant paths. The global and scoped sets equal
// Access(userID) by construction.
func (e *Evaluator) Explain(ctx context.Context, userID string) (Explanation, error) {
	grants, local, external, err := e.load(ctx, e.pool, userID)
	if err != nil {
		return Explanation{}, err
	}
	out, r := explain(grants, local, external, e.scopable)
	scopes, err := e.expand(ctx, r.scoped)
	if err != nil {
		return Explanation{}, err
	}
	kept := out.Scoped[:0]
	for _, sp := range out.Scoped {
		if s, ok := scopes[sp.Name]; ok {
			sp.Locations = s.Locations
			kept = append(kept, sp)
		}
	}
	out.Scoped = kept
	return out, nil
}

func explain(grants []grant, local, external bool, scopable func(string) bool) (Explanation, reduction) {
	known := registryRisk()
	r := reduce(grants, local, external, scopable)
	out := Explanation{Roles: []RoleGrant{}, Permissions: []ExplainedPermission{}, Scoped: []ScopedPermission{}, Inert: []InertGrant{},
		LocalAccount: local, External: external, Excluded: r.excluded}
	by := map[string][]string{}
	for _, g := range grants {
		rg := RoleGrant{RoleID: g.roleID, RoleKey: g.roleKey, RoleName: g.roleName, BuiltInAdmin: g.builtIn, AssignmentID: g.assignmentID,
			Source: "direct", ExpiresAt: g.expiresAt, Scope: g.scope, ScopeLocationID: g.scopeLocationID}
		if g.subjectType == SubjectDirectoryGroup {
			rg.Source, rg.GroupID = "directory_group", g.subjectID
		}
		out.Roles = append(out.Roles, rg)
		if !g.global() {
			continue
		}
		for _, p := range grantPermissions(g, known) {
			if _, ok := r.global[p]; ok {
				by[p] = append(by[p], g.assignmentID)
			}
		}
	}
	for p := range r.global {
		out.Permissions = append(out.Permissions, ExplainedPermission{Name: p, Risk: known[p], GrantedBy: by[p]})
	}
	sort.Slice(out.Permissions, func(i, j int) bool { return out.Permissions[i].Name < out.Permissions[j].Name })
	for p, roots := range r.scoped {
		sp := ScopedPermission{Name: p, Risk: known[p]}
		// Only the roots that survive the cut of expand are effective, so only they are listed (gate 2).
		for _, root := range roots[:min(len(roots), MaxScopedRoots)] {
			sp.Roots = append(sp.Roots, ScopedRoot{RootLocationID: root, GrantedBy: r.scopedBy[p][root]})
		}
		out.Scoped = append(out.Scoped, sp)
	}
	sort.Slice(out.Scoped, func(i, j int) bool { return out.Scoped[i].Name < out.Scoped[j].Name })
	for id, perms := range r.inert {
		out.Inert = append(out.Inert, InertGrant{AssignmentID: id, Permissions: perms})
	}
	sort.Slice(out.Inert, func(i, j int) bool { return out.Inert[i].AssignmentID < out.Inert[j].AssignmentID })
	return out, r
}

// holdings is what the guards need to know about one User (F16 design L5): the global set and global roles exactly
// as before scope, plus the expanded Location sets of scoped holdings. A scoped holding satisfies a guard only for
// a Location inside its expanded set, never globally.
type holdings struct {
	// perms is the global set (Principal.Permissions).
	perms map[string]struct{}
	// scoped maps a scopable permission not held globally to its expanded, sorted Location set (Principal.ScopeOf).
	scoped map[string][]string
	admin  bool
	// roleIDs are the roles held through global assignments.
	roleIDs map[string]struct{}
	// roleScoped maps a role held only through scoped assignments to the expanded, sorted Location set. Scoped
	// assignments of an external account and of the built-in administrator role are left out (as in RoleGrants).
	roleScoped map[string][]string
	// inert are the global-only permissions the User reaches only through scoped assignments (no effect today, but
	// a later registry change could make them effective; dominance treats them like global holdings, gate 3).
	inert map[string]struct{}
	local bool
}

// covers reports whether the User holds permission p globally (locationID "") or at locationID: globally, or p is
// scopable and locationID lies in the expanded set (at or below one of the User's scoped roots for p).
func (h holdings) covers(p, locationID string) bool {
	if _, ok := h.perms[p]; ok {
		return true
	}
	if locationID == "" {
		return false
	}
	_, found := slices.BinarySearch(h.scoped[p], locationID)
	return found
}

// holdsRoleAt reports whether the User holds the role globally, or (locationID set) through a scoped assignment at
// locationID or one of its ancestors.
func (h holdings) holdsRoleAt(roleID, locationID string) bool {
	if _, ok := h.roleIDs[roleID]; ok {
		return true
	}
	if locationID == "" {
		return false
	}
	_, found := slices.BinarySearch(h.roleScoped[roleID], locationID)
	return found
}

// holdsRoleAnywhere reports whether the User holds the role globally or at any Location.
func (h holdings) holdsRoleAnywhere(roleID string) bool {
	_, global := h.roleIDs[roleID]
	return global || len(h.roleScoped[roleID]) > 0
}

// unionPerms is the global set plus every permission held at some Location (separation-of-duties hygiene).
func (h holdings) unionPerms() map[string]struct{} {
	out := make(map[string]struct{}, len(h.perms)+len(h.scoped))
	for p := range h.perms {
		out[p] = struct{}{}
	}
	for p := range h.scoped {
		out[p] = struct{}{}
	}
	return out
}

// holdingsOf evaluates a User inside the caller's transaction. The Location tree is read only when the User has
// scoped holdings, once per distinct set of scoped roots (permissions and roles share the calls).
func (e *Evaluator) holdingsOf(ctx context.Context, q querier, userID string) (holdings, error) {
	grants, local, external, err := e.load(ctx, q, userID)
	if err != nil {
		return holdings{}, err
	}
	r := reduce(grants, local, external, e.scopable)
	h := holdings{perms: r.global, scoped: map[string][]string{}, roleIDs: map[string]struct{}{}, roleScoped: map[string][]string{},
		inert: map[string]struct{}{}, local: local}
	for _, perms := range r.inert {
		for _, p := range perms {
			h.inert[p] = struct{}{}
		}
	}
	roleRoots := map[string]map[string]struct{}{}
	for _, g := range grants {
		switch {
		case g.global():
			h.roleIDs[g.roleID] = struct{}{}
			if g.builtIn && !external {
				h.admin = true
			}
		case g.scope == ScopeLocation && g.scopeLocationID != "" && !g.builtIn && !external:
			if roleRoots[g.roleID] == nil {
				roleRoots[g.roleID] = map[string]struct{}{}
			}
			roleRoots[g.roleID][g.scopeLocationID] = struct{}{}
		}
	}
	// One expansion for permissions and roles: keys are prefixed so a permission and a role id cannot collide.
	toExpand := map[string][]string{}
	for p, roots := range r.scoped {
		toExpand["p:"+p] = roots
	}
	for roleID, set := range roleRoots {
		if _, global := h.roleIDs[roleID]; global {
			continue
		}
		ids := make([]string, 0, len(set))
		for id := range set {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		toExpand["r:"+roleID] = ids
	}
	expanded, err := e.expand(ctx, toExpand)
	if err != nil {
		return holdings{}, err
	}
	for key, s := range expanded {
		if p, ok := strings.CutPrefix(key, "p:"); ok {
			h.scoped[p] = s.Locations
		} else if roleID, ok := strings.CutPrefix(key, "r:"); ok {
			h.roleScoped[roleID] = s.Locations
		}
	}
	return h, nil
}
