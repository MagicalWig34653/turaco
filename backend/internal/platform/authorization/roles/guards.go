package roles

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// Privilege-escalation guards (F14 A8, section 2.5, review rules R2, R6, R10). They live in the role service, not
// in the UI. A System actor (the turaco-admin CLI, the expiry job) is exempt from the actor-relative rules because
// it has no User whose permissions could be compared; it is never exempt from the data rules (no expiry on the
// administrator role, no high-risk permission for a local account).

// actor is who performs a role operation, evaluated inside the operation's transaction.
type actor struct {
	system   bool
	userID   string
	h        holdings
	groupIDs []string
}

// actorOf evaluates the actor of an operation in tx.
func (s *Service) actorOf(ctx context.Context, tx pgx.Tx, a audit.Actor) (actor, error) {
	if a.UserID == "" {
		return actor{system: true}, nil
	}
	h, err := s.eval.holdingsOf(ctx, tx, a.UserID)
	if err != nil {
		return actor{}, fmt.Errorf("evaluate actor: %w", err)
	}
	groups, err := s.subjects.GroupIDsOfUser(ctx, a.UserID)
	if err != nil {
		return actor{}, fmt.Errorf("resolve actor groups: %w", err)
	}
	return actor{userID: a.UserID, h: h, groupIDs: groups}, nil
}

// exempt reports whether the actor-relative ceilings do not apply: system actors and platform administrators.
func (a actor) exempt() bool { return a.system || a.h.admin }

// holdsRoleAt is the role-holding rule (R2) at a scope: locationID "" means a global assignment, which needs the
// role held globally; a scoped assignment at L needs the role held globally or at L or an ancestor of L.
func (a actor) holdsRoleAt(roleID, locationID string) bool {
	return a.h.holdsRoleAt(roleID, locationID)
}

// holdsRoleAnywhere is used by role permission edits: a role the actor holds at any scope cannot gain permissions
// through the actor's own edit.
func (a actor) holdsRoleAnywhere(roleID string) bool { return a.h.holdsRoleAnywhere(roleID) }

// checkGrant is the grant ceiling at a scope (F16 design L5): the actor hands out only permissions they cover there
// (locationID "": held globally; a Location: held globally, or scopable and held at the Location or an ancestor),
// and high-risk permissions only as a platform administrator (403 access.grant_exceeds_holder,
// access.high_risk_requires_administrator). A global-only permission of a role assigned at a Location must be held
// globally although it is inert there, so a later registry change cannot turn it into an uncovered grant. Role
// permission edits affect every scope and pass "".
func (a actor) checkGrant(perms []string, locationID string) error {
	if a.exempt() {
		return nil
	}
	risk := registryRisk()
	for _, p := range perms {
		if risk[p] == RiskHigh {
			return ErrHighRiskNeedsAdministrator
		}
	}
	for _, p := range perms {
		if !a.h.covers(p, locationID) {
			return ErrGrantExceedsHolder
		}
	}
	return nil
}

// checkRemoval is the removal ceiling (R6) at a scope: taking permissions away from holders needs an actor who
// covers them all there (locationID "" for a global assignment and for role permission edits).
func (a actor) checkRemoval(perms []string, locationID string) error {
	if a.exempt() {
		return nil
	}
	for _, p := range perms {
		if !a.h.covers(p, locationID) {
			return ErrGrantExceedsHolder
		}
	}
	return nil
}

// checkNotSelf refuses a change to the actor's own assignments: the actor's User or a Directory Group the actor
// currently belongs to. Platform administrators are not exempt; the CLI is.
func (a actor) checkNotSelf(subjectType, subjectID string) error {
	if a.system {
		return nil
	}
	if subjectType == SubjectUser && strings.EqualFold(subjectID, a.userID) {
		return ErrSelfAssignment
	}
	if subjectType == SubjectDirectoryGroup && slices.ContainsFunc(a.groupIDs, func(g string) bool { return strings.EqualFold(g, subjectID) }) {
		return ErrSelfAssignment
	}
	return nil
}

func hasHighRisk(perms []string) bool {
	risk := registryRisk()
	return slices.ContainsFunc(perms, func(p string) bool { return risk[p] == RiskHigh })
}

// userIsLocal reports whether the User has a local credential (R10).
func userIsLocal(ctx context.Context, q querier, userID string) (bool, error) {
	var local bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM platform.local_credentials WHERE user_id = $1::uuid AND kind = 'local')`, userID).Scan(&local)
	if err != nil {
		return false, fmt.Errorf("check local account: %w", err)
	}
	return local, nil
}

// roleHeldByLocalAccount reports whether an active assignment gives the role to a User with a local credential.
func roleHeldByLocalAccount(ctx context.Context, q querier, roleID string) (bool, error) {
	var held bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM platform.role_assignments a
			JOIN platform.local_credentials c ON c.user_id = a.subject_id AND c.kind = 'local'
			WHERE a.role_id = $1 AND a.subject_type = 'user' AND a.revoked_at IS NULL
			  AND (a.expires_at IS NULL OR a.expires_at > now()))`, roleID).Scan(&held)
	if err != nil {
		return false, fmt.Errorf("check local assignees: %w", err)
	}
	return held, nil
}

const maxAckReasonLength = 500

// requireAcknowledgement checks that every newly violated rule is acknowledged with a reason and records the
// acknowledgement in the operation's transaction. It returns the acknowledged rule keys.
func (s *Service) requireAcknowledgement(ctx context.Context, tx pgx.Tx, actor audit.Actor, corr, targetType, targetID string,
	violated []SoDRule, ack Acknowledgement) error {
	if len(violated) == 0 {
		return nil
	}
	keys := make([]string, len(violated))
	for i, r := range violated {
		keys[i] = r.Key
	}
	reason := strings.TrimSpace(ack.Reason)
	if reason == "" || utf8.RuneCountInString(reason) > maxAckReasonLength || safetext.ContainsUnsafe(reason, true) {
		return &SoDRequiredError{Rules: keys}
	}
	for _, k := range keys {
		if !slices.Contains(ack.Rules, k) {
			return &SoDRequiredError{Rules: keys}
		}
	}
	return s.record(ctx, tx, actor, corr, "authorization.role.sod_acknowledged", targetType, targetID, nil, nil,
		map[string]any{"rules": keys, "reason": reason})
}

// newViolations returns the rules violated by after that before did not already violate.
func newViolations(before, after map[string]struct{}) []SoDRule {
	was := map[string]bool{}
	for _, r := range Violations(before) {
		was[r.Key] = true
	}
	var out []SoDRule
	for _, r := range Violations(after) {
		if !was[r.Key] {
			out = append(out, r)
		}
	}
	return out
}

// PermissionDiff is the audit and API view of a permission change.
type PermissionDiff struct {
	Added   []string
	Removed []string
}

// DiffPermissions compares two sorted permission lists.
func DiffPermissions(before, after []string) PermissionDiff {
	b, a := permSet(before), permSet(after)
	var d PermissionDiff
	for p := range a {
		if _, ok := b[p]; !ok {
			d.Added = append(d.Added, p)
		}
	}
	for p := range b {
		if _, ok := a[p]; !ok {
			d.Removed = append(d.Removed, p)
		}
	}
	sort.Strings(d.Added)
	sort.Strings(d.Removed)
	return d
}

func riskCounts(perms []string) map[string]int {
	risk := registryRisk()
	out := map[string]int{}
	for _, p := range perms {
		out[risk[p]]++
	}
	return out
}

func (d PermissionDiff) auditMap(templateKey string) map[string]any {
	m := map[string]any{"added": nonNil(d.Added), "removed": nonNil(d.Removed),
		"addedByRisk": riskCounts(d.Added), "removedByRisk": riskCounts(d.Removed)}
	if templateKey != "" {
		m["templateKey"] = templateKey
	}
	return m
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ---------------------------------------------------------------------------------------------------------------
// Guards is the contract other modules use for the account-takeover and last-administrator rules of user
// operations (review rules R1 and R6). It runs inside the caller's transaction, so the decision and the change are
// one unit.

// Guards answers authorization questions for operations on Users.
type Guards struct {
	eval     *Evaluator
	subjects SubjectDirectory
}

// NewGuards creates the guards over the same evaluator logic as the role service.
func NewGuards(pool *pgxpool.Pool, subjects SubjectDirectory) *Guards {
	return &Guards{eval: NewEvaluator(pool, subjects), subjects: subjects}
}

// ErrDominanceRequired means the actor is neither a platform administrator nor a holder of all effective
// permissions of the target.
var ErrDominanceRequired = errors.New("authorization: the actor does not dominate the target account")

// IsAdministrator reports whether the User holds the built-in administrator role.
func (g *Guards) IsAdministrator(ctx context.Context, tx pgx.Tx, userID string) (bool, error) {
	h, err := g.eval.holdingsOf(ctx, tx, userID)
	if err != nil {
		return false, err
	}
	return h.admin, nil
}

// RequireDominance implements R1: the actor must be a platform administrator or cover every effective holding of
// the target: each permission the target holds globally the actor holds globally, and for each permission the
// target holds at Locations the actor holds it globally or at every one of those Locations (F16 design L5). A
// system actor (CLI) is the operator and passes. Evaluated in tx.
func (g *Guards) RequireDominance(ctx context.Context, tx pgx.Tx, a audit.Actor, targetUserID string) error {
	if a.UserID == "" {
		return nil
	}
	actorH, err := g.eval.holdingsOf(ctx, tx, a.UserID)
	if err != nil {
		return err
	}
	if actorH.admin {
		return nil
	}
	target, err := g.eval.holdingsOf(ctx, tx, targetUserID)
	if err != nil {
		return err
	}
	if !dominates(actorH, target) {
		return ErrDominanceRequired
	}
	return nil
}

// dominates is the R1 comparison of two evaluated Users.
func dominates(actor, target holdings) bool {
	if actor.admin {
		return true
	}
	if target.admin {
		return false
	}
	for p := range target.perms {
		if !actor.covers(p, "") {
			return false
		}
	}
	for p, locs := range target.scoped {
		if actor.covers(p, "") {
			continue
		}
		for _, l := range locs {
			if !actor.covers(p, l) {
				return false
			}
		}
	}
	return true
}

// HoldsAnyRole reports whether the User has an active role assignment of their own (not through a group).
func (g *Guards) HoldsAnyRole(ctx context.Context, tx pgx.Tx, userID string) (bool, error) {
	var held bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM platform.role_assignments
		               WHERE subject_type = 'user' AND subject_id = $1::uuid AND revoked_at IS NULL)`, userID).Scan(&held)
	if err != nil {
		return false, fmt.Errorf("check role assignments: %w", err)
	}
	return held, nil
}

// WouldLoseLastAdministrator reports whether making the User inactive would leave no active User with a direct
// platform-administrator assignment. It takes the administrator role lock first (the lock order of the role
// service), so two concurrent deactivations of the last two administrators serialize and the second one is
// refused. The caller holds the Organization user row lock and keeps both locks until its transaction ends.
func (g *Guards) WouldLoseLastAdministrator(ctx context.Context, tx pgx.Tx, userID string) (bool, error) {
	var roleID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM platform.roles WHERE key = $1 AND built_in AND deleted_at IS NULL FOR UPDATE`,
		AdministratorRoleKey).Scan(&roleID); err != nil {
		return false, fmt.Errorf("lock administrator role: %w", err)
	}
	rows, err := tx.Query(ctx, `
		SELECT subject_id::text FROM platform.role_assignments
		WHERE role_id = $1 AND subject_type = 'user' AND revoked_at IS NULL`, roleID)
	if err != nil {
		return false, fmt.Errorf("list administrators: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return false, fmt.Errorf("list administrators: scan: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("list administrators: %w", err)
	}
	if !slices.ContainsFunc(ids, func(id string) bool { return strings.EqualFold(id, userID) }) {
		return false, nil
	}
	active, err := g.subjects.ActiveUsers(ctx, ids)
	if err != nil {
		return false, fmt.Errorf("check active administrators: %w", err)
	}
	if !active[strings.ToLower(userID)] && !active[userID] {
		// The User is not an active administrator already; nothing is lost.
		return false, nil
	}
	for _, id := range ids {
		if !strings.EqualFold(id, userID) && active[id] {
			return false, nil
		}
	}
	return true, nil
}
