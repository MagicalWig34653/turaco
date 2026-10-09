package roles

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

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
// Permissions and Explain share one query (load) and one reduction (reduce),
// so the explanation can never disagree with the decision.
type Evaluator struct {
	pool   *pgxpool.Pool
	groups GroupResolver
}

// NewEvaluator creates the evaluator.
func NewEvaluator(pool *pgxpool.Pool, groups GroupResolver) *Evaluator {
	return &Evaluator{pool: pool, groups: groups}
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
}

// activeAssignmentSQL selects the unexpired assignments that apply to $1 (a User) or $2 (the User's Directory
// Groups) with their live roles.
const grantsSQL = `
	SELECT r.id::text, r.key, r.name, r.built_in,
	       COALESCE((SELECT array_agg(rp.permission) FROM platform.role_permissions rp WHERE rp.role_id = r.id), '{}'::text[]),
	       a.id::text, a.subject_type, a.subject_id::text, a.expires_at,
	       EXISTS (SELECT 1 FROM platform.local_credentials c WHERE c.user_id = $1::uuid AND c.kind = 'local')
	FROM platform.role_assignments a
	JOIN platform.roles r ON r.id = a.role_id AND r.deleted_at IS NULL
	WHERE a.revoked_at IS NULL AND a.scope = 'global' AND (a.expires_at IS NULL OR a.expires_at > now()) AND (
		(a.subject_type = 'user' AND a.subject_id = $1::uuid)
		OR (a.subject_type = 'directory_group' AND a.subject_id = ANY($2::text[]::uuid[])))
	ORDER BY a.id`

func (e *Evaluator) load(ctx context.Context, q querier, userID string) (grants []grant, local bool, err error) {
	if !uuidPattern.MatchString(userID) {
		return nil, false, nil
	}
	groupIDs, err := e.groups.GroupIDsOfUser(ctx, userID)
	if err != nil {
		return nil, false, fmt.Errorf("resolve groups: %w", err)
	}
	if groupIDs == nil {
		groupIDs = []string{}
	}
	rows, err := q.Query(ctx, grantsSQL, userID, groupIDs)
	if err != nil {
		return nil, false, fmt.Errorf("load role permissions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var g grant
		if err := rows.Scan(&g.roleID, &g.roleKey, &g.roleName, &g.builtIn, &g.perms, &g.assignmentID, &g.subjectType, &g.subjectID,
			&g.expiresAt, &local); err != nil {
			return nil, false, fmt.Errorf("load role permissions: scan: %w", err)
		}
		grants = append(grants, g)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("load role permissions: %w", err)
	}
	return grants, local, nil
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
		return out
	}
	out := make([]string, 0, len(g.perms))
	for _, p := range g.perms {
		if _, ok := known[p]; ok {
			out = append(out, p)
		}
	}
	return out
}

// reduce is the one place that turns grants into the effective set. excluded lists the high-risk permissions the
// local-account rule removed.
func reduce(grants []grant, local bool) (set map[string]struct{}, excluded []string) {
	known := registryRisk()
	set = map[string]struct{}{}
	removed := map[string]struct{}{}
	for _, g := range grants {
		for _, p := range grantPermissions(g, known) {
			if local && known[p] == RiskHigh {
				removed[p] = struct{}{}
				continue
			}
			set[p] = struct{}{}
		}
	}
	for p := range removed {
		if _, still := set[p]; !still {
			excluded = append(excluded, p)
		}
	}
	sort.Strings(excluded)
	return set, excluded
}

// RiskHigh is the registry risk class of permissions that only a platform administrator may hand out.
const RiskHigh = "high"

// Permissions returns the effective permission set of userID.
func (e *Evaluator) Permissions(ctx context.Context, userID string) (map[string]struct{}, error) {
	return e.PermissionsIn(ctx, e.pool, userID)
}

// PermissionsIn is Permissions inside the caller's transaction (guards evaluate the actor and the target in the
// transaction that changes them).
func (e *Evaluator) PermissionsIn(ctx context.Context, q querier, userID string) (map[string]struct{}, error) {
	grants, local, err := e.load(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	set, _ := reduce(grants, local)
	return set, nil
}

// RoleIDs returns the ids of the active roles that apply to the User (assigned to the User or to one of the User's
// Directory Groups). Modules use them to resolve grants that name a role.
func (e *Evaluator) RoleIDs(ctx context.Context, userID string) ([]string, error) {
	grants, _, err := e.load(ctx, e.pool, userID)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	out := []string{}
	for _, g := range grants {
		if _, dup := seen[g.roleID]; dup {
			continue
		}
		seen[g.roleID] = struct{}{}
		out = append(out, g.roleID)
	}
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
}

// ExplainedPermission is one effective permission with the assignments that grant it.
type ExplainedPermission struct {
	Name string
	Risk string
	// GrantedBy are assignment ids into Explanation.Roles.
	GrantedBy []string
}

// Explanation is the effective permission set with its grant paths.
type Explanation struct {
	Roles       []RoleGrant
	Permissions []ExplainedPermission
	// LocalAccount is true for a User with a local credential; Excluded lists the high-risk permissions its roles
	// would grant and the rule removed.
	LocalAccount bool
	Excluded     []string
}

// Explain returns the effective permissions of userID with their grant paths. The permission set equals
// Permissions(userID) by construction.
func (e *Evaluator) Explain(ctx context.Context, userID string) (Explanation, error) {
	grants, local, err := e.load(ctx, e.pool, userID)
	if err != nil {
		return Explanation{}, err
	}
	return explain(grants, local), nil
}

func explain(grants []grant, local bool) Explanation {
	known := registryRisk()
	set, excluded := reduce(grants, local)
	out := Explanation{Roles: []RoleGrant{}, Permissions: []ExplainedPermission{}, LocalAccount: local, Excluded: excluded}
	by := map[string][]string{}
	for _, g := range grants {
		rg := RoleGrant{RoleID: g.roleID, RoleKey: g.roleKey, RoleName: g.roleName, BuiltInAdmin: g.builtIn, AssignmentID: g.assignmentID,
			Source: "direct", ExpiresAt: g.expiresAt}
		if g.subjectType == SubjectDirectoryGroup {
			rg.Source, rg.GroupID = "directory_group", g.subjectID
		}
		out.Roles = append(out.Roles, rg)
		for _, p := range grantPermissions(g, known) {
			if _, ok := set[p]; ok {
				by[p] = append(by[p], g.assignmentID)
			}
		}
	}
	for p := range set {
		out.Permissions = append(out.Permissions, ExplainedPermission{Name: p, Risk: known[p], GrantedBy: by[p]})
	}
	sort.Slice(out.Permissions, func(i, j int) bool { return out.Permissions[i].Name < out.Permissions[j].Name })
	return out
}

// holdings is what the guards need to know about one User.
type holdings struct {
	perms   map[string]struct{}
	admin   bool
	roleIDs map[string]struct{}
	local   bool
}

// holdingsOf evaluates a User inside the caller's transaction.
func (e *Evaluator) holdingsOf(ctx context.Context, q querier, userID string) (holdings, error) {
	grants, local, err := e.load(ctx, q, userID)
	if err != nil {
		return holdings{}, err
	}
	h := holdings{roleIDs: map[string]struct{}{}, local: local}
	h.perms, _ = reduce(grants, local)
	for _, g := range grants {
		h.roleIDs[g.roleID] = struct{}{}
		if g.builtIn {
			h.admin = true
		}
	}
	return h, nil
}
