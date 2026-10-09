package roles

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

// Access reads (F14 section 2.3): effective permissions of a User with grant paths, holders of a permission and
// effective members of a role. They reveal who can do what, so the transport requires an elevated permission.

// EffectivePermissions is Explain plus the separation-of-duties warnings of the User.
type EffectivePermissions struct {
	Explanation
	Warnings []SoDRule
}

// EffectivePermissionsOf explains userID. A platform administrator holds everything by design and gets no
// separation-of-duties warnings.
func (s *Service) EffectivePermissionsOf(ctx context.Context, userID string) (EffectivePermissions, error) {
	if !uuidPattern.MatchString(userID) {
		return EffectivePermissions{}, ErrNotFound
	}
	if ok, err := s.subjects.UserExists(ctx, userID); err != nil {
		return EffectivePermissions{}, fmt.Errorf("check user: %w", err)
	} else if !ok {
		return EffectivePermissions{}, ErrNotFound
	}
	ex, err := s.eval.Explain(ctx, userID)
	if err != nil {
		return EffectivePermissions{}, err
	}
	out := EffectivePermissions{Explanation: ex}
	if !slices.ContainsFunc(ex.Roles, func(r RoleGrant) bool { return r.BuiltInAdmin }) {
		set := map[string]struct{}{}
		for _, p := range ex.Permissions {
			set[p.Name] = struct{}{}
		}
		out.Warnings = Violations(set)
	}
	return out, nil
}

// Holder is one assignment that grants a permission.
type Holder struct {
	AssignmentID       string
	RoleID             string
	RoleKey            string
	RoleName           string
	BuiltInAdmin       bool
	SubjectType        string
	SubjectID          string
	SubjectDisplayName string
	ExpiresAt          *time.Time
}

// HolderPage is one page of holders.
type HolderPage struct {
	Items      []Holder
	NextCursor string
}

// Holders lists the active assignments whose role grants the permission: Users directly and Directory Groups
// (their members are listed with the group). Newest first.
func (s *Service) Holders(ctx context.Context, permission string, page Page) (HolderPage, error) {
	if _, ok := registryRisk()[permission]; !ok {
		return HolderPage{}, &UnknownPermissionError{Names: []string{permission}}
	}
	page = page.Normalize()
	args := []any{permission}
	cursor := ""
	if page.Cursor != "" {
		if !uuidPattern.MatchString(page.Cursor) {
			return HolderPage{}, ErrInvalidCursor
		}
		args = append(args, page.Cursor)
		cursor = " AND a.id < $2"
	}
	args = append(args, page.Limit+1)
	rows, err := s.pool.Query(ctx, `
		SELECT a.id::text, r.id::text, r.key, r.name, r.built_in, a.subject_type, a.subject_id::text, a.expires_at
		FROM platform.role_assignments a
		JOIN platform.roles r ON r.id = a.role_id AND r.deleted_at IS NULL
		WHERE a.revoked_at IS NULL AND (a.expires_at IS NULL OR a.expires_at > now()) AND a.scope = 'global'
		  AND (r.built_in OR EXISTS (SELECT 1 FROM platform.role_permissions rp WHERE rp.role_id = r.id AND rp.permission = $1))`+cursor+
		fmt.Sprintf(` ORDER BY a.id DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return HolderPage{}, fmt.Errorf("list holders: %w", err)
	}
	defer rows.Close()
	var res HolderPage
	for rows.Next() {
		var h Holder
		if err := rows.Scan(&h.AssignmentID, &h.RoleID, &h.RoleKey, &h.RoleName, &h.BuiltInAdmin, &h.SubjectType, &h.SubjectID, &h.ExpiresAt); err != nil {
			return HolderPage{}, fmt.Errorf("list holders: scan: %w", err)
		}
		res.Items = append(res.Items, h)
	}
	if err := rows.Err(); err != nil {
		return HolderPage{}, fmt.Errorf("list holders: %w", err)
	}
	if len(res.Items) > page.Limit {
		res.Items = res.Items[:page.Limit]
		res.NextCursor = res.Items[page.Limit-1].AssignmentID
	}
	if res.Items == nil {
		res.Items = []Holder{}
	}
	var users, groups []string
	for _, h := range res.Items {
		if h.SubjectType == SubjectUser {
			users = append(users, h.SubjectID)
		} else {
			groups = append(groups, h.SubjectID)
		}
	}
	if names, err := s.subjects.DisplayNames(ctx, users, groups); err == nil {
		for i := range res.Items {
			res.Items[i].SubjectDisplayName = names[res.Items[i].SubjectID]
		}
	}
	return res, nil
}

// MaxRoleMembers caps the effective members of a role returned in one read.
const MaxRoleMembers = 500

// Member is a User who holds a role.
type Member struct {
	UserID      string
	DisplayName string
	// Source is "direct" or "directory_group".
	Source string
}

// MemberList is the effective members of a role: Users assigned directly and the members of assigned Directory
// Groups (with observed nesting), capped at MaxRoleMembers; Capped says there are more.
type MemberList struct {
	Items  []Member
	Capped bool
}

// MembersOf returns the effective members of a role.
func (s *Service) MembersOf(ctx context.Context, roleID string) (MemberList, error) {
	if !uuidPattern.MatchString(roleID) {
		return MemberList{}, ErrNotFound
	}
	if _, err := loadRole(ctx, s.pool, "r.id = $1", roleID); err != nil {
		return MemberList{}, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT subject_type, subject_id::text FROM platform.role_assignments
		WHERE role_id = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now()) ORDER BY id`, roleID)
	if err != nil {
		return MemberList{}, fmt.Errorf("list role assignments: %w", err)
	}
	var direct, groups []string
	for rows.Next() {
		var typ, id string
		if err := rows.Scan(&typ, &id); err != nil {
			rows.Close()
			return MemberList{}, fmt.Errorf("list role assignments: scan: %w", err)
		}
		if typ == SubjectUser {
			direct = append(direct, id)
		} else {
			groups = append(groups, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return MemberList{}, fmt.Errorf("list role assignments: %w", err)
	}
	var viaGroup []string
	if len(groups) > 0 {
		if viaGroup, err = s.subjects.GroupMemberUserIDs(ctx, groups, MaxRoleMembers+1); err != nil {
			return MemberList{}, fmt.Errorf("expand groups: %w", err)
		}
	}
	seen := map[string]bool{}
	var out MemberList
	add := func(id, source string) {
		if seen[id] {
			return
		}
		seen[id] = true
		if len(out.Items) >= MaxRoleMembers {
			out.Capped = true
			return
		}
		out.Items = append(out.Items, Member{UserID: id, Source: source})
	}
	for _, id := range direct {
		add(id, "direct")
	}
	for _, id := range viaGroup {
		add(id, SubjectDirectoryGroup)
	}
	if out.Items == nil {
		out.Items = []Member{}
	}
	ids := make([]string, len(out.Items))
	for i, m := range out.Items {
		ids[i] = m.UserID
	}
	if names, err := s.subjects.DisplayNames(ctx, ids, nil); err == nil {
		for i := range out.Items {
			out.Items[i].DisplayName = names[out.Items[i].UserID]
		}
	}
	sort.SliceStable(out.Items, func(i, j int) bool { return out.Items[i].DisplayName < out.Items[j].DisplayName })
	return out, nil
}

// TemplateView is a Role Template with its difference to the existing roles.
type TemplateView struct {
	Template
	// Roles lists the roles created from the template with the permission drift to the current template.
	Roles []TemplateRole
}

// TemplateRole is a role created from a template and how it differs from the template today.
type TemplateRole struct {
	RoleID          string
	RoleKey         string
	Name            string
	TemplateVersion int
	// Missing are template permissions the role does not hold; Extra are permissions the role holds beyond it.
	Missing []string
	Extra   []string
}

// ListTemplates returns the built-in templates with the roles created from them.
func (s *Service) ListTemplates(ctx context.Context) ([]TemplateView, error) {
	rs, err := s.ListRoles(ctx)
	if err != nil {
		return nil, err
	}
	out := []TemplateView{}
	for _, t := range Templates() {
		v := TemplateView{Template: t, Roles: []TemplateRole{}}
		for _, r := range rs {
			if r.TemplateKey != t.Key {
				continue
			}
			d := DiffPermissions(r.Permissions, t.Permissions)
			v.Roles = append(v.Roles, TemplateRole{RoleID: r.ID, RoleKey: r.Key, Name: r.Name, TemplateVersion: r.TemplateVersion,
				Missing: nonNil(d.Added), Extra: nonNil(d.Removed)})
		}
		out = append(out, v)
	}
	return out, nil
}

// Expiry job of role assignments (core, always on): the evaluator ignores an expired assignment immediately; this
// job writes the revocation and its audit event so history shows the end.
const (
	ExpireJobType    = "access.expire_assignments"
	ExpireJobTimeout = 2 * time.Minute
	ExpireInterval   = time.Minute
	expireBatch      = 200
)

// ExpireAssignments revokes the assignments whose expiry passed (one batch per call) as system actor
// access-expiry and audits each. It returns how many were revoked. It is idempotent.
func ExpireAssignments(ctx context.Context, pool *pgxpool.Pool, now time.Time) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT a.id::text, a.role_id::text, a.subject_type, a.subject_id::text, a.expires_at
			FROM platform.role_assignments a
			WHERE a.revoked_at IS NULL AND a.expires_at IS NOT NULL AND a.expires_at <= $1
			ORDER BY a.id LIMIT $2 FOR UPDATE OF a SKIP LOCKED`, now, expireBatch)
		if err != nil {
			return fmt.Errorf("find expired assignments: %w", err)
		}
		type due struct {
			id, roleID, typ, subject string
			expires                  time.Time
		}
		var list []due
		for rows.Next() {
			var d due
			if err := rows.Scan(&d.id, &d.roleID, &d.typ, &d.subject, &d.expires); err != nil {
				rows.Close()
				return fmt.Errorf("find expired assignments: scan: %w", err)
			}
			list = append(list, d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("find expired assignments: %w", err)
		}
		actor := audit.SystemActor("access-expiry")
		corr := "access-expiry-" + now.UTC().Format("20060102T150405")
		for _, d := range list {
			if _, err := tx.Exec(ctx, `UPDATE platform.role_assignments SET revoked_at = now(), revoked_by = $2 WHERE id = $1 AND revoked_at IS NULL`,
				d.id, []byte(actorRef(actor))); err != nil {
				return fmt.Errorf("revoke expired assignment: %w", err)
			}
			if err := audit.Record(ctx, tx, audit.Change{Action: "authorization.role.assignment_expired", TargetType: "role_assignment", TargetID: d.id,
				Actor: actor, CorrelationID: corr,
				Metadata: map[string]any{"roleId": d.roleID, "subjectType": d.typ, "subjectId": d.subject, "expiredAt": d.expires.UTC().Format(time.RFC3339)}}); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// NewExpireHandler is the handler of ExpireJobType. A backlog larger than one batch is worked off by the
// following runs.
func NewExpireHandler(pool *pgxpool.Pool) jobs.Handler {
	return func(ctx context.Context, _ jobs.Job) error {
		_, err := ExpireAssignments(ctx, pool, time.Now())
		return err
	}
}
