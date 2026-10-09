package views

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ---------------------------------------------------------------- the user's pins and the sidebar

type pinRow struct {
	viewID   string
	group    string
	position int
	hidden   bool
	source   string
}

// pinState merges the caller's own pins (including hidden overrides) with the Pin Rules that apply to the caller's
// Teams and roles, keeps only Views the caller may currently use (share, not archived, active owner, readable
// resource, module on) and orders them by group, position and name. A Pin Rule shows a View; it never grants it.
func (s *Service) pinState(ctx context.Context, c Caller) ([]PinEntry, error) {
	vw, err := s.viewerOf(ctx, c)
	if err != nil {
		return nil, err
	}
	merged := map[string]pinRow{}
	rows, err := s.pool.Query(ctx, `SELECT view_id::text, group_key, position, hidden FROM views.pins WHERE user_id = $1::uuid`, c.UserID)
	if err != nil {
		return nil, fmt.Errorf("load pins: %w", err)
	}
	for rows.Next() {
		p := pinRow{source: "user"}
		if err := rows.Scan(&p.viewID, &p.group, &p.position, &p.hidden); err != nil {
			rows.Close()
			return nil, fmt.Errorf("load pins: scan: %w", err)
		}
		merged[p.viewID] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load pins: %w", err)
	}
	rules, err := s.pool.Query(ctx, `
		SELECT view_id::text, group_key, position FROM views.pin_rules
		WHERE (subject_type = 'team' AND subject_id = ANY($1::text[]::uuid[])) OR (subject_type = 'role' AND subject_id = ANY($2::text[]::uuid[]))
		ORDER BY position, id`, nonNil(vw.teams), nonNil(vw.roles))
	if err != nil {
		return nil, fmt.Errorf("load pin rules: %w", err)
	}
	for rules.Next() {
		p := pinRow{source: "rule"}
		if err := rules.Scan(&p.viewID, &p.group, &p.position); err != nil {
			rules.Close()
			return nil, fmt.Errorf("load pin rules: scan: %w", err)
		}
		if _, own := merged[p.viewID]; !own {
			merged[p.viewID] = p
		}
	}
	rules.Close()
	if err := rules.Err(); err != nil {
		return nil, fmt.Errorf("load pin rules: %w", err)
	}
	ids := make([]string, 0, len(merged))
	for id := range merged {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	acc, err := loadAccessBatch(ctx, s.pool, ids, vw)
	if err != nil {
		return nil, err
	}
	ownerSet := map[string]bool{}
	for _, a := range acc {
		if a.level != AccessOwner {
			ownerSet[a.view.OwnerID] = true
		}
	}
	owners := make([]string, 0, len(ownerSet))
	for o := range ownerSet {
		owners = append(owners, o)
	}
	active, err := s.dir.ActiveUsers(ctx, owners)
	if err != nil {
		return nil, fmt.Errorf("check owners: %w", err)
	}
	enabled := map[string]bool{}
	var out []PinEntry
	for _, id := range ids {
		a, ok := acc[id]
		if !ok {
			continue
		}
		res, ok := s.res.canUse(c, a.view.Resource)
		if !ok || (a.level != AccessOwner && !active[a.view.OwnerID]) {
			continue
		}
		if _, known := enabled[res.Module]; !known {
			on, err := s.gate.Enabled(ctx, res.Module)
			if err != nil {
				return nil, fmt.Errorf("check module: %w", err)
			}
			enabled[res.Module] = on
		}
		if !enabled[res.Module] {
			continue
		}
		p := merged[id]
		out = append(out, PinEntry{ViewID: id, Name: a.view.Name, Resource: a.view.Resource, GroupKey: p.group, Position: p.position,
			Hidden: p.hidden, Source: p.source})
	}
	slices.SortFunc(out, func(a, b PinEntry) int {
		if d := strings.Compare(a.GroupKey, b.GroupKey); d != 0 {
			return d
		}
		if a.Position != b.Position {
			return a.Position - b.Position
		}
		if d := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); d != 0 {
			return d
		}
		return strings.Compare(a.ViewID, b.ViewID)
	})
	return out, nil
}

// Pins returns the caller's pin state including hidden entries, for the "Manage pins" dialog.
func (s *Service) Pins(ctx context.Context, c Caller) ([]PinEntry, error) {
	return s.pinState(ctx, c)
}

// SidebarGroup is one collapsible sidebar group.
type SidebarGroup struct {
	Key   string
	Items []PinEntry
}

// Sidebar is the caller's pinned Views grouped for the shell, plus the collapsed groups. Counts are not part of
// it (they arrive with the Queue slice).
type Sidebar struct {
	Groups    []SidebarGroup
	Collapsed []string
}

// Sidebar returns the visible (not hidden) pins grouped by group key.
func (s *Service) Sidebar(ctx context.Context, c Caller) (Sidebar, error) {
	entries, err := s.pinState(ctx, c)
	if err != nil {
		return Sidebar{}, err
	}
	out := Sidebar{Groups: []SidebarGroup{}, Collapsed: []string{}}
	for _, e := range entries {
		if e.Hidden {
			continue
		}
		if n := len(out.Groups); n == 0 || out.Groups[n-1].Key != e.GroupKey {
			out.Groups = append(out.Groups, SidebarGroup{Key: e.GroupKey})
		}
		g := &out.Groups[len(out.Groups)-1]
		g.Items = append(g.Items, e)
	}
	var collapsed []string
	err = s.pool.QueryRow(ctx, `SELECT collapsed_groups FROM views.sidebar_state WHERE user_id = $1::uuid`, c.UserID).Scan(&collapsed)
	if err != nil && err != pgx.ErrNoRows {
		return Sidebar{}, fmt.Errorf("load sidebar state: %w", err)
	}
	if collapsed != nil {
		out.Collapsed = collapsed
	}
	return out, nil
}

// ReplacePins replaces the caller's own pin rows. Every View must be one the caller may use now; otherwise
// nothing changes and the answer is the same as for an unknown View.
func (s *Service) ReplacePins(ctx context.Context, c Caller, in []PinInput) error {
	if len(in) > MaxPinsPerUser {
		return ErrLimitReached
	}
	vw, err := s.viewerOf(ctx, c)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(in))
	for i := range in {
		p := &in[i]
		if !isUUID(p.ViewID) {
			return ErrNotFound
		}
		p.ViewID = strings.ToLower(p.ViewID)
		if seen[p.ViewID] {
			return invalid("A view is pinned twice.")
		}
		seen[p.ViewID] = true
		if !s.res.groups[p.GroupKey] {
			return invalid("Unknown sidebar group.")
		}
		if p.Position < 0 || p.Position > 10000 {
			return invalid("The position is out of range.")
		}
		ids = append(ids, p.ViewID)
	}
	acc, err := loadAccessBatch(ctx, s.pool, ids, vw)
	if err != nil {
		return err
	}
	var others []string
	for _, a := range acc {
		if a.level != AccessOwner {
			others = append(others, a.view.OwnerID)
		}
	}
	active, err := s.dir.ActiveUsers(ctx, others)
	if err != nil {
		return fmt.Errorf("check owners: %w", err)
	}
	for _, id := range ids {
		a, ok := acc[id]
		if !ok {
			return ErrNotFound
		}
		res, ok := s.res.canUse(c, a.view.Resource)
		if !ok || (a.level != AccessOwner && !active[a.view.OwnerID]) {
			return ErrNotFound
		}
		if err := s.moduleOn(ctx, res); err != nil {
			return err
		}
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM views.pins WHERE user_id = $1::uuid`, c.UserID); err != nil {
			return fmt.Errorf("clear pins: %w", err)
		}
		for _, p := range in {
			if _, err := tx.Exec(ctx, `INSERT INTO views.pins (user_id, view_id, group_key, position, hidden) VALUES ($1::uuid, $2::uuid, $3, $4, $5)`,
				c.UserID, p.ViewID, p.GroupKey, p.Position, p.Hidden); err != nil {
				return fmt.Errorf("insert pin: %w", err)
			}
		}
		return nil
	})
}

// SetSidebarState stores which sidebar groups the caller collapsed.
func (s *Service) SetSidebarState(ctx context.Context, c Caller, collapsed []string) error {
	if !isUUID(c.UserID) {
		return ErrForbidden
	}
	if len(collapsed) > maxCollapsedGroup {
		return invalid("Too many groups.")
	}
	seen := map[string]bool{}
	clean := make([]string, 0, len(collapsed))
	for _, g := range collapsed {
		if !groupKeyPattern.MatchString(g) {
			return invalid("A group key is invalid.")
		}
		if !seen[g] {
			seen[g] = true
			clean = append(clean, g)
		}
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO views.sidebar_state (user_id, collapsed_groups) VALUES ($1::uuid, $2::text[])
		ON CONFLICT (user_id) DO UPDATE SET collapsed_groups = EXCLUDED.collapsed_groups, updated_at = now()`, c.UserID, clean)
	if err != nil {
		return fmt.Errorf("store sidebar state: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------- Pin Rules (views.pin_for_groups)

// PinRuleInput creates a Pin Rule.
type PinRuleInput struct {
	SubjectType string
	SubjectID   string
	GroupKey    string
	Position    int
}

// requirePinForGroups is the ONE permission that governs pinning for groups. views.publish (sharing with
// everyone) and views.share do not imply it, and it does not imply them.
func requirePinForGroups(c Caller) error {
	if !c.Has(PermPinForGroups) {
		return ErrForbidden
	}
	return nil
}

// CreatePinRule shows a View in the sidebar of the members of a Team or role. The creator must be able to use the
// View themself; the rule grants nothing: members who cannot use the View never see it.
func (s *Service) CreatePinRule(ctx context.Context, c Caller, viewID string, in PinRuleInput) (PinRule, error) {
	if err := requirePinForGroups(c); err != nil {
		return PinRule{}, err
	}
	v, acc, _, err := s.loadAccessible(ctx, c, viewID)
	if err != nil {
		return PinRule{}, err
	}
	if !canRun(acc) || v.ArchivedAt != nil {
		return PinRule{}, ErrNotFound
	}
	if in.SubjectType != SubjectTeam && in.SubjectType != SubjectRole {
		return PinRule{}, invalid("A pin rule subject is a team or a role.")
	}
	if !isUUID(in.SubjectID) {
		return PinRule{}, invalid("A pin rule subject must be an id.")
	}
	in.SubjectID = strings.ToLower(in.SubjectID)
	if !s.res.groups[in.GroupKey] {
		return PinRule{}, invalid("Unknown sidebar group.")
	}
	if in.Position < 0 || in.Position > 10000 {
		return PinRule{}, invalid("The position is out of range.")
	}
	var teams, roles []string
	if in.SubjectType == SubjectTeam {
		teams = []string{in.SubjectID}
	} else {
		roles = []string{in.SubjectID}
	}
	if err := s.checkSubjects(ctx, nil, teams, roles); err != nil {
		return PinRule{}, err
	}
	var rule PinRule
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Serialize rule changes of one View so the limit holds.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('views.rules:' || $1, 0))`, viewID); err != nil {
			return fmt.Errorf("lock rules: %w", err)
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM views.pin_rules WHERE view_id = $1::uuid`, viewID).Scan(&n); err != nil {
			return fmt.Errorf("count rules: %w", err)
		}
		row := tx.QueryRow(ctx, `
			INSERT INTO views.pin_rules (view_id, subject_type, subject_id, group_key, position, created_by)
			VALUES ($1::uuid, $2, $3::uuid, $4, $5, $6::uuid)
			ON CONFLICT (view_id, subject_type, subject_id) DO UPDATE SET group_key = EXCLUDED.group_key, position = EXCLUDED.position
			RETURNING id::text, view_id::text, subject_type, subject_id::text, group_key, position, created_by::text, created_at, (xmax = 0)`,
			viewID, in.SubjectType, in.SubjectID, in.GroupKey, in.Position, c.UserID)
		var inserted bool
		if err := row.Scan(&rule.ID, &rule.ViewID, &rule.SubjectType, &rule.SubjectID, &rule.GroupKey, &rule.Position, &rule.CreatedBy, &rule.CreatedAt, &inserted); err != nil {
			return fmt.Errorf("insert pin rule: %w", err)
		}
		if inserted && n >= MaxRulesPerView {
			return ErrLimitReached
		}
		return s.audit(ctx, tx, c, "views.pin_rule.created", viewID, map[string]any{"resource": v.Resource, "ruleId": rule.ID,
			"subjectType": rule.SubjectType, "subjectId": rule.SubjectID, "group": rule.GroupKey})
	})
	return rule, err
}

// PinRules lists the Pin Rules of a View the caller may use.
func (s *Service) PinRules(ctx context.Context, c Caller, viewID string) ([]PinRule, error) {
	if err := requirePinForGroups(c); err != nil {
		return nil, err
	}
	if _, acc, _, err := s.loadAccessible(ctx, c, viewID); err != nil {
		return nil, err
	} else if !canRun(acc) {
		return nil, ErrNotFound
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, view_id::text, subject_type, subject_id::text, group_key, position, created_by::text, created_at
		FROM views.pin_rules WHERE view_id = $1::uuid ORDER BY position, id`, viewID)
	if err != nil {
		return nil, fmt.Errorf("list pin rules: %w", err)
	}
	defer rows.Close()
	out := []PinRule{}
	for rows.Next() {
		var r PinRule
		if err := rows.Scan(&r.ID, &r.ViewID, &r.SubjectType, &r.SubjectID, &r.GroupKey, &r.Position, &r.CreatedBy, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("list pin rules: scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeletePinRule removes a Pin Rule of the named View. The rule must belong to that View (a rule id of another
// View answers not found).
func (s *Service) DeletePinRule(ctx context.Context, c Caller, viewID, ruleID string) error {
	if err := requirePinForGroups(c); err != nil {
		return err
	}
	v, acc, _, err := s.loadAccessible(ctx, c, viewID)
	if err != nil {
		return err
	}
	if !canRun(acc) {
		return ErrNotFound
	}
	if !isUUID(ruleID) {
		return ErrNotFound
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var subjectType, subjectID string
		err := tx.QueryRow(ctx, `DELETE FROM views.pin_rules WHERE id = $1::uuid AND view_id = $2::uuid RETURNING subject_type, subject_id::text`,
			ruleID, viewID).Scan(&subjectType, &subjectID)
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("delete pin rule: %w", err)
		}
		return s.audit(ctx, tx, c, "views.pin_rule.deleted", viewID, map[string]any{"resource": v.Resource, "ruleId": ruleID,
			"subjectType": subjectType, "subjectId": subjectID})
	})
}
