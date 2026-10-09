package views

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

// Service implements Saved Views, Shares, Pins and Pin Rules.
type Service struct {
	pool   *pgxpool.Pool
	dir    Directory
	runner Runner
	gate   ModuleGate
	res    resourceSet
	now    func() time.Time

	providers []SystemProvider
	counts    *countCache
}

// NewService builds the service over the registered resources.
func NewService(pool *pgxpool.Pool, dir Directory, runner Runner, gate ModuleGate, resources []Resource) (*Service, error) {
	rs, err := newResourceSet(resources)
	if err != nil {
		return nil, err
	}
	return &Service{pool: pool, dir: dir, runner: runner, gate: gate, res: rs, now: time.Now, counts: newCountCache()}, nil
}

func (c Caller) corr() string {
	if c.CorrelationID != "" {
		return c.CorrelationID
	}
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "views-" + hex.EncodeToString(b)
}

func (s *Service) viewerOf(ctx context.Context, c Caller) (viewer, error) {
	if !isUUID(c.UserID) {
		return viewer{}, ErrForbidden
	}
	teams, err := s.dir.CurrentTeamIDs(ctx, c.UserID)
	if err != nil {
		return viewer{}, fmt.Errorf("resolve teams: %w", err)
	}
	groups, err := s.dir.GroupIDsOfUser(ctx, c.UserID)
	if err != nil {
		return viewer{}, fmt.Errorf("resolve groups: %w", err)
	}
	roles, err := roleIDs(ctx, s.pool, c.UserID, groups)
	if err != nil {
		return viewer{}, err
	}
	return viewer{userID: c.UserID, teams: teams, roles: roles}, nil
}

func (s *Service) moduleOn(ctx context.Context, r Resource) error {
	on, err := s.gate.Enabled(ctx, r.Module)
	if err != nil {
		return fmt.Errorf("check module %s: %w", r.Module, err)
	}
	if !on {
		return ErrModuleDisabled
	}
	return nil
}

// access decides what the caller may do with a View it loaded with the given share level. It returns ErrNotFound
// for every case the caller may not know about: no share, archived (unless owner or administrator), a resource the
// caller cannot read, or an owner who was deactivated (the View stops being shared with anyone).
func (s *Service) access(ctx context.Context, c Caller, v View, level string) (string, error) {
	admin := c.Has(PermAdmin)
	if level == "" {
		if !admin {
			return "", ErrNotFound
		}
		return AccessAdmin, nil
	}
	if v.ArchivedAt != nil && level != AccessOwner && !admin {
		return "", ErrNotFound
	}
	if level != AccessOwner {
		if _, ok := s.res.canUse(c, v.Resource); !ok {
			return "", ErrNotFound
		}
		active, err := s.dir.ActiveUsers(ctx, []string{v.OwnerID})
		if err != nil {
			return "", fmt.Errorf("check owner: %w", err)
		}
		if !active[v.OwnerID] {
			if admin {
				return AccessAdmin, nil
			}
			return "", ErrNotFound
		}
	}
	return level, nil
}

// loadAccessible loads a View and resolves the caller's access.
func (s *Service) loadAccessible(ctx context.Context, c Caller, id string) (View, string, viewer, error) {
	if !isUUID(id) {
		return View{}, "", viewer{}, ErrNotFound
	}
	vw, err := s.viewerOf(ctx, c)
	if err != nil {
		return View{}, "", viewer{}, err
	}
	v, level, err := loadView(ctx, s.pool, id, vw, false)
	if err != nil {
		return View{}, "", viewer{}, err
	}
	acc, err := s.access(ctx, c, v, level)
	if err != nil {
		return View{}, "", viewer{}, err
	}
	return v, acc, vw, nil
}

// canRun reports whether the access level lets the caller use the View's data (owner, editor or user; an
// administrator without a share does not).
func canRun(access string) bool {
	return access == AccessOwner || access == AccessEdit || access == AccessUse
}

func (s *Service) info(ctx context.Context, c Caller, v View, access string, withShares bool) (ViewInfo, error) {
	out := ViewInfo{View: v, Access: access}
	if r, ok := s.res.byKey[v.Resource]; ok {
		on, err := s.gate.Enabled(ctx, r.Module)
		if err != nil {
			return ViewInfo{}, fmt.Errorf("check module: %w", err)
		}
		out.ModuleEnabled = on
	}
	names, err := s.dir.UserNames(ctx, []string{v.OwnerID})
	if err != nil {
		return ViewInfo{}, fmt.Errorf("resolve owner name: %w", err)
	}
	out.OwnerName = names[v.OwnerID]
	pinned, err := pinnedSet(ctx, s.pool, c.UserID, []string{v.ID})
	if err != nil {
		return ViewInfo{}, err
	}
	out.Pinned = pinned[v.ID]
	if withShares && (access == AccessOwner || access == AccessAdmin) {
		if out.Shares, err = listShares(ctx, s.pool, v.ID); err != nil {
			return ViewInfo{}, err
		}
	}
	return out, nil
}

// validate dry-runs the definition's filter through the owning module's query endpoint as the caller, so a
// definition is accepted only when the caller could run it (known and permitted fields, valid operators and
// values, cost limits). One row is read.
func (s *Service) validate(ctx context.Context, c Caller, resource string, d Definition) error {
	if d.Filter == nil {
		return nil
	}
	_, err := s.runner.Query(ctx, c, resource, query.Request{Filter: d.Filter, Limit: 1})
	return err
}

func (s *Service) audit(ctx context.Context, tx pgx.Tx, c Caller, action, viewID string, meta map[string]any) error {
	if err := audit.Record(ctx, tx, audit.Change{Action: action, TargetType: "view", TargetID: viewID, Actor: audit.UserActor(c.UserID),
		CorrelationID: c.corr(), Metadata: meta}); err != nil {
		return fmt.Errorf("audit %s: %w", action, err)
	}
	return nil
}

func (s *Service) publish(ctx context.Context, tx pgx.Tx, c Caller, event string, payload map[string]any) error {
	uid := c.UserID
	if err := events.Publish(ctx, tx, events.Publication{Type: event, ActorID: &uid, CorrelationID: c.corr(), Payload: payload}); err != nil {
		return fmt.Errorf("publish %s: %w", event, err)
	}
	return nil
}

func viewMeta(v View) map[string]any {
	return map[string]any{"resource": v.Resource, "version": v.Version, "conditions": conditionCount(v.Definition.Filter),
		"columns": len(v.Definition.Columns)}
}

// ---------------------------------------------------------------- create, read, list

// CreateInput creates a private View.
type CreateInput struct {
	Resource    string
	Name        string
	Description string
	Definition  Definition
}

// Create stores a new private View owned by the caller.
func (s *Service) Create(ctx context.Context, c Caller, in CreateInput) (ViewInfo, error) {
	if !isUUID(c.UserID) {
		return ViewInfo{}, ErrForbidden
	}
	res, ok := s.res.byKey[in.Resource]
	if !ok {
		return ViewInfo{}, invalid("Unknown resource.")
	}
	if !c.hasAny(res.Use) {
		return ViewInfo{}, ErrForbidden
	}
	if err := s.moduleOn(ctx, res); err != nil {
		return ViewInfo{}, err
	}
	name, err := cleanName(in.Name)
	if err != nil {
		return ViewInfo{}, err
	}
	desc, err := cleanDescription(in.Description)
	if err != nil {
		return ViewInfo{}, err
	}
	def, raw, hash, err := canonical(in.Definition)
	if err != nil {
		return ViewInfo{}, err
	}
	if err := s.validate(ctx, c, res.Key, def); err != nil {
		return ViewInfo{}, err
	}
	return s.insert(ctx, c, res.Key, name, desc, def, raw, hash, "created", nil)
}

func (s *Service) insert(ctx context.Context, c Caller, resource, name, desc string, def Definition, raw []byte, hash, action string, extra map[string]any) (ViewInfo, error) {
	var created View
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Serialize creations of one owner so the per-owner limit cannot be passed by concurrent requests.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('views.owner:' || $1, 0))`, c.UserID); err != nil {
			return fmt.Errorf("lock owner: %w", err)
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM views.saved_views WHERE owner_user_id = $1::uuid AND archived_at IS NULL`, c.UserID).Scan(&n); err != nil {
			return fmt.Errorf("count views: %w", err)
		}
		if n >= MaxViewsPerOwner {
			return ErrLimitReached
		}
		var id string
		err := tx.QueryRow(ctx, `
			INSERT INTO views.saved_views (resource, name, description, owner_user_id, definition, definition_hash, schema_version, last_edited_by)
			VALUES ($1, $2, $3, $4::uuid, $5::jsonb, $6, $7, $4::uuid) RETURNING id::text`,
			resource, name, desc, c.UserID, raw, hash, schemaVersion).Scan(&id)
		if isUnique(err) {
			return ErrNameTaken
		}
		if err != nil {
			return fmt.Errorf("insert view: %w", err)
		}
		if created, _, err = loadView(ctx, tx, id, viewer{userID: c.UserID}, false); err != nil {
			return err
		}
		meta := viewMeta(created)
		for k, v := range extra {
			meta[k] = v
		}
		return s.audit(ctx, tx, c, "views.view."+action, created.ID, meta)
	})
	if err != nil {
		return ViewInfo{}, err
	}
	return s.info(ctx, c, created, AccessOwner, true)
}

// Get returns one View the caller may see.
func (s *Service) Get(ctx context.Context, c Caller, id string) (ViewInfo, error) {
	v, acc, _, err := s.loadAccessible(ctx, c, id)
	if err != nil {
		return ViewInfo{}, err
	}
	return s.info(ctx, c, v, acc, true)
}

// ListInput filters GET /views.
type ListInput struct {
	Resource string
	// Scope is "all" (default: mine plus shared with me), "mine", "shared" or "admin" (views.admin: every View).
	Scope    string
	Archived bool
	Cursor   string
	Limit    int
}

// ListOutput is one page of Views.
type ListOutput struct {
	Items      []ViewInfo
	NextCursor string
}

// List returns the Views the caller may see, newest first.
func (s *Service) List(ctx context.Context, c Caller, in ListInput) (ListOutput, error) {
	vw, err := s.viewerOf(ctx, c)
	if err != nil {
		return ListOutput{}, err
	}
	if in.Scope == "" {
		in.Scope = "all"
	}
	if !slices.Contains([]string{"all", "mine", "shared", "admin"}, in.Scope) {
		return ListOutput{}, invalid("Unknown scope.")
	}
	if in.Scope == "admin" && !c.Has(PermAdmin) {
		return ListOutput{}, ErrForbidden
	}
	if in.Resource != "" {
		if _, ok := s.res.byKey[in.Resource]; !ok {
			return ListOutput{}, invalid("Unknown resource.")
		}
	}
	if in.Cursor != "" && !isUUID(in.Cursor) {
		return ListOutput{}, invalid("The cursor is invalid.")
	}
	limit := in.Limit
	if limit <= 0 {
		limit = DefaultListPage
	}
	limit = min(limit, MaxListPage)
	resources := s.res.usable(c)
	if in.Scope == "admin" {
		resources = resources[:0]
		for k := range s.res.byKey {
			resources = append(resources, k)
		}
		slices.Sort(resources)
	}
	rows, err := listViews(ctx, s.pool, vw, listFilter{resources: resources, resource: in.Resource, scope: in.Scope,
		archived: in.Archived, afterID: in.Cursor, limit: limit})
	if err != nil {
		return ListOutput{}, err
	}
	out := ListOutput{Items: []ViewInfo{}}
	if len(rows) > limit {
		rows = rows[:limit]
		out.NextCursor = rows[len(rows)-1].view.ID
	}
	// Views of deactivated owners stop being shared with anyone.
	ownerSet := map[string]bool{}
	for _, r := range rows {
		ownerSet[r.view.OwnerID] = true
	}
	owners := make([]string, 0, len(ownerSet))
	for o := range ownerSet {
		owners = append(owners, o)
	}
	active, err := s.dir.ActiveUsers(ctx, owners)
	if err != nil {
		return ListOutput{}, fmt.Errorf("check owners: %w", err)
	}
	names, err := s.dir.UserNames(ctx, owners)
	if err != nil {
		return ListOutput{}, fmt.Errorf("resolve owner names: %w", err)
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.view.ID)
	}
	pinned, err := pinnedSet(ctx, s.pool, c.UserID, ids)
	if err != nil {
		return ListOutput{}, err
	}
	enabled := map[string]bool{}
	for _, r := range rows {
		if r.level != AccessOwner && !active[r.view.OwnerID] && !(in.Scope == "admin") {
			continue
		}
		acc := r.level
		if acc == "" {
			acc = AccessAdmin
		}
		if _, known := enabled[r.view.Resource]; !known {
			on, err := s.gate.Enabled(ctx, s.res.byKey[r.view.Resource].Module)
			if err != nil {
				return ListOutput{}, fmt.Errorf("check module: %w", err)
			}
			enabled[r.view.Resource] = on
		}
		out.Items = append(out.Items, ViewInfo{View: r.view, Access: acc, OwnerName: names[r.view.OwnerID],
			ModuleEnabled: enabled[r.view.Resource], Pinned: pinned[r.view.ID]})
	}
	return out, nil
}

// ---------------------------------------------------------------- change

// UpdateInput changes name, description and/or definition. ExpectedVersion is required.
type UpdateInput struct {
	ExpectedVersion int
	Name            *string
	Description     *string
	Definition      *Definition
}

// Update changes a View the caller owns or may edit.
func (s *Service) Update(ctx context.Context, c Caller, id string, in UpdateInput) (ViewInfo, error) {
	if in.ExpectedVersion < 1 {
		return ViewInfo{}, invalid("expectedVersion is required.")
	}
	if in.Name == nil && in.Description == nil && in.Definition == nil {
		return ViewInfo{}, invalid("Nothing to change.")
	}
	v, acc, vw, err := s.loadAccessible(ctx, c, id)
	if err != nil {
		return ViewInfo{}, err
	}
	if acc != AccessOwner && acc != AccessEdit {
		return ViewInfo{}, ErrForbidden
	}
	if v.ArchivedAt != nil {
		return ViewInfo{}, ErrArchived
	}
	res, ok := s.res.canUse(c, v.Resource)
	if !ok {
		return ViewInfo{}, ErrNotFound
	}
	if err := s.moduleOn(ctx, res); err != nil {
		return ViewInfo{}, err
	}
	name, desc := v.Name, v.Description
	if in.Name != nil {
		if name, err = cleanName(*in.Name); err != nil {
			return ViewInfo{}, err
		}
	}
	if in.Description != nil {
		if desc, err = cleanDescription(*in.Description); err != nil {
			return ViewInfo{}, err
		}
	}
	def, raw, hash := v.Definition, []byte(nil), v.Hash
	if in.Definition != nil {
		if def, raw, hash, err = canonical(*in.Definition); err != nil {
			return ViewInfo{}, err
		}
		if err := s.validate(ctx, c, res.Key, def); err != nil {
			return ViewInfo{}, err
		}
	}
	var updated View
	var level string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		locked, lvl, err := loadView(ctx, tx, id, vw, true)
		if err != nil {
			return err
		}
		// The share set may have changed since the preliminary read: re-derive the level under the row lock.
		if lvl != AccessOwner && lvl != AccessEdit {
			if lvl == "" {
				return ErrNotFound
			}
			return ErrForbidden
		}
		if locked.ArchivedAt != nil {
			return ErrArchived
		}
		if locked.Version != in.ExpectedVersion {
			return ErrConflict
		}
		nameChanged := (in.Name != nil && name != locked.Name) || (in.Description != nil && desc != locked.Description)
		defChanged := in.Definition != nil && hash != locked.Hash
		if !nameChanged && !defChanged {
			updated, level = locked, lvl
			return nil
		}
		args := []any{id, name, desc, c.UserID}
		set := `name = $2, description = $3, last_edited_by = $4::uuid, version = version + 1, updated_at = now()`
		if defChanged {
			args = append(args, raw, hash)
			set += `, definition = $5::jsonb, definition_hash = $6`
		}
		if _, err := tx.Exec(ctx, `UPDATE views.saved_views SET `+set+` WHERE id = $1::uuid`, args...); err != nil {
			if isUnique(err) {
				return ErrNameTaken
			}
			return fmt.Errorf("update view: %w", err)
		}
		if updated, level, err = loadView(ctx, tx, id, vw, false); err != nil {
			return err
		}
		meta := viewMeta(updated)
		if nameChanged {
			if err := s.audit(ctx, tx, c, "views.view.renamed", id, meta); err != nil {
				return err
			}
		}
		if defChanged {
			return s.audit(ctx, tx, c, "views.view.definition_changed", id, meta)
		}
		return nil
	})
	if err != nil {
		return ViewInfo{}, err
	}
	return s.info(ctx, c, updated, level, true)
}

// Duplicate copies a View the caller may use into a private View owned by the caller.
func (s *Service) Duplicate(ctx context.Context, c Caller, id string) (ViewInfo, error) {
	v, acc, _, err := s.loadAccessible(ctx, c, id)
	if err != nil {
		return ViewInfo{}, err
	}
	if !canRun(acc) {
		return ViewInfo{}, ErrNotFound
	}
	if v.ArchivedAt != nil {
		return ViewInfo{}, ErrArchived
	}
	res, ok := s.res.canUse(c, v.Resource)
	if !ok {
		return ViewInfo{}, ErrNotFound
	}
	if err := s.moduleOn(ctx, res); err != nil {
		return ViewInfo{}, err
	}
	def, raw, hash, err := canonical(v.Definition)
	if err != nil {
		return ViewInfo{}, err
	}
	for attempt := 1; attempt <= 5; attempt++ {
		suffix := " (copy)"
		if attempt > 1 {
			suffix = fmt.Sprintf(" (copy %d)", attempt)
		}
		name := truncateRunes(v.Name, MaxNameLength-utf8.RuneCountInString(suffix)) + suffix
		out, err := s.insert(ctx, c, v.Resource, name, v.Description, def, raw, hash, "created", map[string]any{"duplicatedFrom": v.ID})
		if errors.Is(err, ErrNameTaken) {
			continue
		}
		return out, err
	}
	return ViewInfo{}, ErrNameTaken
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// ---------------------------------------------------------------- lifecycle

type lifecycleOp struct {
	action string
	// allowed decides from the locked row and the caller's level whether the operation may run.
	allowed func(c Caller, v View, level string) error
	// sql is the SET clause (besides last_edited_by, version and updated_at); $2 is the acting User id.
	sql string
	// event is published after the change, if set.
	event func(v View) (string, map[string]any)
	meta  func(before, after View) map[string]any
}

func ownerOrAdmin(c Caller, _ View, level string) error {
	if level == AccessOwner || c.Has(PermAdmin) {
		return nil
	}
	if level == "" {
		return ErrNotFound
	}
	return ErrForbidden
}

var (
	opArchive = lifecycleOp{action: "views.view.archived", allowed: func(c Caller, v View, l string) error {
		if err := ownerOrAdmin(c, v, l); err != nil {
			return err
		}
		if v.ArchivedAt != nil {
			return ErrArchived
		}
		return nil
	}, sql: `archived_at = now()`, event: func(v View) (string, map[string]any) {
		return "ViewArchived", map[string]any{"viewId": v.ID, "resource": v.Resource}
	}}
	opRestore = lifecycleOp{action: "views.view.restored", allowed: func(c Caller, v View, l string) error {
		if err := ownerOrAdmin(c, v, l); err != nil {
			return err
		}
		if v.ArchivedAt == nil {
			return invalid("The view is not archived.")
		}
		return nil
	}, sql: `archived_at = NULL`}
	opTakeOver = lifecycleOp{action: "views.view.ownership_taken", allowed: func(c Caller, v View, _ string) error {
		if !c.Has(PermAdmin) {
			return ErrForbidden
		}
		if v.OwnerID == c.UserID {
			return invalid("You already own this view.")
		}
		return nil
	}, sql: `owner_user_id = $2::uuid`,
		meta: func(before, _ View) map[string]any { return map[string]any{"previousOwnerId": before.OwnerID} }}
)

// Archive archives a View (owner or views.admin). Shares stop working while it is archived.
func (s *Service) Archive(ctx context.Context, c Caller, id string, expectedVersion int) (ViewInfo, error) {
	return s.lifecycle(ctx, c, id, expectedVersion, opArchive)
}

// Restore restores an archived View (owner or views.admin).
func (s *Service) Restore(ctx context.Context, c Caller, id string, expectedVersion int) (ViewInfo, error) {
	return s.lifecycle(ctx, c, id, expectedVersion, opRestore)
}

// TakeOver makes the calling administrator (views.admin) the owner, for example after the owner left.
func (s *Service) TakeOver(ctx context.Context, c Caller, id string, expectedVersion int) (ViewInfo, error) {
	return s.lifecycle(ctx, c, id, expectedVersion, opTakeOver)
}

func (s *Service) lifecycle(ctx context.Context, c Caller, id string, expected int, op lifecycleOp) (ViewInfo, error) {
	if expected < 1 {
		return ViewInfo{}, invalid("expectedVersion is required.")
	}
	if _, _, _, err := s.loadAccessible(ctx, c, id); err != nil {
		return ViewInfo{}, err
	}
	vw, err := s.viewerOf(ctx, c)
	if err != nil {
		return ViewInfo{}, err
	}
	var after View
	var level string
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		before, lvl, err := loadView(ctx, tx, id, vw, true)
		if err != nil {
			return err
		}
		if err := op.allowed(c, before, lvl); err != nil {
			return err
		}
		if before.Version != expected {
			return ErrConflict
		}
		_, err = tx.Exec(ctx, `UPDATE views.saved_views SET `+op.sql+`, last_edited_by = $2::uuid, version = version + 1, updated_at = now() WHERE id = $1::uuid`,
			id, c.UserID)
		if isUnique(err) {
			return ErrNameTaken
		}
		if err != nil {
			return fmt.Errorf("%s: %w", op.action, err)
		}
		if after, level, err = loadView(ctx, tx, id, vw, false); err != nil {
			return err
		}
		meta := viewMeta(after)
		if op.meta != nil {
			for k, v := range op.meta(before, after) {
				meta[k] = v
			}
		}
		if err := s.audit(ctx, tx, c, op.action, id, meta); err != nil {
			return err
		}
		if op.event != nil {
			ev, payload := op.event(after)
			return s.publish(ctx, tx, c, ev, payload)
		}
		return nil
	})
	if err != nil {
		return ViewInfo{}, err
	}
	acc := level
	if acc == "" {
		acc = AccessAdmin
	}
	return s.info(ctx, c, after, acc, true)
}

// Retention job (core, always on): archived Views are deleted for good after ArchiveRetention.
const (
	PurgeJobType    = "views.purge_archived"
	PurgeJobTimeout = 2 * time.Minute
	PurgeInterval   = 24 * time.Hour
)

// PurgeArchived hard-deletes Views archived longer than ArchiveRetention (at most one batch per call) and audits
// each deletion as the retention actor. It returns how many Views were deleted.
func (s *Service) PurgeArchived(ctx context.Context) (int, error) {
	return PurgeArchived(ctx, s.pool, s.now())
}

// PurgeArchived is the retention operation without a Service (the worker has no Directory or Runner).
func PurgeArchived(ctx context.Context, pool *pgxpool.Pool, now time.Time) (int, error) {
	var n int
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		gone, err := purgeArchived(ctx, tx, now.Add(-ArchiveRetention))
		if err != nil {
			return err
		}
		n = len(gone)
		corr := "views-purge-" + now.UTC().Format("20060102T150405")
		for _, g := range gone {
			if err := audit.Record(ctx, tx, audit.Change{Action: "views.view.purged", TargetType: "view", TargetID: g[0],
				Actor: audit.SystemActor("views-retention"), CorrelationID: corr, Metadata: map[string]any{"resource": g[1]}}); err != nil {
				return fmt.Errorf("audit purge: %w", err)
			}
		}
		return nil
	})
	return n, err
}

// NewPurgeHandler is the handler of PurgeJobType. Each run deletes one batch; a backlog is worked off by the
// following runs, and the job is idempotent.
func NewPurgeHandler(pool *pgxpool.Pool) jobs.Handler {
	return func(ctx context.Context, _ jobs.Job) error {
		_, err := PurgeArchived(ctx, pool, time.Now())
		return err
	}
}

// ---------------------------------------------------------------- shares

// ShareInput is one entry of the full share set.
type ShareInput struct {
	SubjectType string
	SubjectID   string
	Level       string
}

type shareKey struct{ typ, id string }

// SetShares replaces the share set of a View (owner or views.admin). The new set may add shares only with the
// permissions the design requires (views.share for users, Teams and roles; views.publish for everyone); removing
// shares needs none. Subjects must exist and be active.
func (s *Service) SetShares(ctx context.Context, c Caller, id string, expected int, in []ShareInput) (ViewInfo, error) {
	if expected < 1 {
		return ViewInfo{}, invalid("expectedVersion is required.")
	}
	if len(in) > MaxSharesPerView {
		return ViewInfo{}, ErrLimitReached
	}
	v, acc, vw, err := s.loadAccessible(ctx, c, id)
	if err != nil {
		return ViewInfo{}, err
	}
	if acc != AccessOwner && acc != AccessAdmin {
		return ViewInfo{}, ErrForbidden
	}
	want := map[shareKey]string{}
	var users, teams, roles []string
	for _, sh := range in {
		k := shareKey{sh.SubjectType, sh.SubjectID}
		if sh.Level != LevelUse && sh.Level != LevelEdit {
			return ViewInfo{}, invalid("A share level is use or edit.")
		}
		switch sh.SubjectType {
		case SubjectEveryone:
			if sh.SubjectID != "" || sh.Level != LevelUse {
				return ViewInfo{}, invalid("An everyone share has no subject and the level use.")
			}
		case SubjectUser, SubjectTeam, SubjectRole:
			if !isUUID(sh.SubjectID) {
				return ViewInfo{}, invalid("A share subject must be an id.")
			}
			k.id = strings.ToLower(sh.SubjectID)
		default:
			return ViewInfo{}, invalid("A share subject type is user, team, role or everyone.")
		}
		if _, dup := want[k]; dup {
			return ViewInfo{}, invalid("A share subject appears twice.")
		}
		want[k] = sh.Level
		switch sh.SubjectType {
		case SubjectUser:
			if k.id == v.OwnerID {
				return ViewInfo{}, invalid("A view cannot be shared with its owner.")
			}
			users = append(users, k.id)
		case SubjectTeam:
			teams = append(teams, k.id)
		case SubjectRole:
			roles = append(roles, k.id)
		}
	}
	if err := s.checkSubjects(ctx, users, teams, roles); err != nil {
		return ViewInfo{}, err
	}
	var after View
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		locked, lvl, err := loadView(ctx, tx, id, vw, true)
		if err != nil {
			return err
		}
		if lvl != AccessOwner && !c.Has(PermAdmin) {
			if lvl == "" {
				return ErrNotFound
			}
			return ErrForbidden
		}
		if locked.ArchivedAt != nil {
			return ErrArchived
		}
		if locked.Version != expected {
			return ErrConflict
		}
		current, err := listShares(ctx, tx, id)
		if err != nil {
			return err
		}
		have := map[shareKey]string{}
		for _, sh := range current {
			have[shareKey{sh.SubjectType, strings.ToLower(sh.SubjectID)}] = sh.Level
		}
		var added, removed []shareKey
		for k, lv := range want {
			if old, ok := have[k]; !ok || old != lv {
				added = append(added, k)
			}
		}
		for k := range have {
			if _, ok := want[k]; !ok {
				removed = append(removed, k)
			}
		}
		if len(added) == 0 && len(removed) == 0 {
			after = locked
			return nil
		}
		for _, k := range added {
			need := PermShare
			if k.typ == SubjectEveryone {
				need = PermPublish
			}
			if !c.Has(need) {
				return ErrForbidden
			}
		}
		for _, k := range removed {
			if _, err := tx.Exec(ctx, `DELETE FROM views.view_shares WHERE view_id = $1::uuid AND subject_type = $2
				AND COALESCE(subject_id, '00000000-0000-0000-0000-000000000000'::uuid) = COALESCE(NULLIF($3, '')::uuid, '00000000-0000-0000-0000-000000000000'::uuid)`,
				id, k.typ, k.id); err != nil {
				return fmt.Errorf("delete share: %w", err)
			}
		}
		for _, k := range added {
			if _, err := tx.Exec(ctx, `
				INSERT INTO views.view_shares (view_id, subject_type, subject_id, level, granted_by)
				VALUES ($1::uuid, $2, NULLIF($3, '')::uuid, $4, $5::uuid)
				ON CONFLICT (view_id, subject_type, COALESCE(subject_id, '00000000-0000-0000-0000-000000000000'::uuid))
				DO UPDATE SET level = EXCLUDED.level, granted_by = EXCLUDED.granted_by, granted_at = now()`,
				id, k.typ, k.id, want[k], c.UserID); err != nil {
				return fmt.Errorf("insert share: %w", err)
			}
		}
		visibility := VisibilityPrivate
		if len(want) > 0 {
			visibility = VisibilityShared
		}
		if _, err := tx.Exec(ctx, `UPDATE views.saved_views SET visibility = $2, version = version + 1, updated_at = now() WHERE id = $1::uuid`, id, visibility); err != nil {
			return fmt.Errorf("update visibility: %w", err)
		}
		if after, _, err = loadView(ctx, tx, id, vw, false); err != nil {
			return err
		}
		for _, k := range removed {
			if err := s.audit(ctx, tx, c, "views.share.revoked", id, map[string]any{"resource": after.Resource, "version": after.Version,
				"subjectType": k.typ, "subjectId": k.id, "level": have[k]}); err != nil {
				return err
			}
		}
		for _, k := range added {
			if err := s.audit(ctx, tx, c, "views.share.granted", id, map[string]any{"resource": after.Resource, "version": after.Version,
				"subjectType": k.typ, "subjectId": k.id, "level": want[k]}); err != nil {
				return err
			}
			if k.typ == SubjectEveryone {
				if err := s.audit(ctx, tx, c, "views.published", id, map[string]any{"resource": after.Resource, "version": after.Version}); err != nil {
					return err
				}
			}
			if err := s.publish(ctx, tx, c, "ViewShared", map[string]any{"viewId": id, "resource": after.Resource,
				"subjectType": k.typ, "subjectId": k.id, "level": want[k]}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ViewInfo{}, err
	}
	return s.info(ctx, c, after, acc, true)
}

func (s *Service) checkSubjects(ctx context.Context, users, teams, roles []string) error {
	if len(users) > 0 {
		active, err := s.dir.ActiveUsers(ctx, users)
		if err != nil {
			return fmt.Errorf("check users: %w", err)
		}
		for _, id := range users {
			if !active[id] {
				return ErrSubjectNotFound
			}
		}
	}
	if len(teams) > 0 {
		active, err := s.dir.ActiveTeams(ctx, teams)
		if err != nil {
			return fmt.Errorf("check teams: %w", err)
		}
		for _, id := range teams {
			if !active[id] {
				return ErrSubjectNotFound
			}
		}
	}
	if len(roles) > 0 {
		exist, err := rolesExist(ctx, s.pool, roles)
		if err != nil {
			return err
		}
		for _, id := range roles {
			if !exist[id] {
				return ErrSubjectNotFound
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------- execution

// ResultsInput selects the page of a View's results.
type ResultsInput struct {
	Cursor string
	Limit  int
	Count  bool
}

// ResultsOutput is a page of results plus what the viewer needs to interpret it.
type ResultsOutput struct {
	View Reference
	Result
}

// Reference identifies the View and definition version the results belong to.
type Reference struct {
	ID       string
	Name     string
	NameKey  string
	Resource string
	Version  int
}

// Results runs a View as the caller. Every execution re-checks, in this order: that the caller may see the View
// (owner or an applicable share, not archived, owner active), that the caller may read the resource, that the
// owning module is on, and then runs the stored definition through the module's own query endpoint, which applies
// the caller's own scope and redaction. Conditions the caller may no longer use evaluate to "no rows" and are
// reported as warnings. After the query the access and definition version are read again: a share that was revoked
// or a definition that changed while the query ran discards the result instead of returning it.
func (s *Service) Results(ctx context.Context, c Caller, id string, in ResultsInput) (ResultsOutput, error) {
	if IsSystemKey(id) {
		return s.resultsSystem(ctx, c, id, in)
	}
	v, acc, vw, err := s.loadAccessible(ctx, c, id)
	if err != nil {
		return ResultsOutput{}, err
	}
	if !canRun(acc) {
		return ResultsOutput{}, ErrNotFound
	}
	if v.ArchivedAt != nil {
		return ResultsOutput{}, ErrArchived
	}
	res, ok := s.res.canUse(c, v.Resource)
	if !ok {
		return ResultsOutput{}, ErrNotFound
	}
	if err := s.moduleOn(ctx, res); err != nil {
		return ResultsOutput{}, err
	}
	ref := Reference{ID: v.ID, Name: v.Name, Resource: v.Resource, Version: v.Version}
	req := query.Request{Cursor: in.Cursor, Limit: in.Limit, Count: in.Count}
	var warnings []query.Warning
	if v.Definition.Filter != nil {
		info, err := s.runner.Fields(ctx, c, res.Key)
		if err != nil {
			return ResultsOutput{}, err
		}
		f, w, empty := degrade(*v.Definition.Filter, info)
		warnings = w
		if empty {
			return ResultsOutput{View: ref, Result: Result{Items: []byte("[]"), Warnings: warnings}}, nil
		}
		req.Filter = &f
	}
	out, err := s.runner.Query(ctx, c, res.Key, req)
	if err != nil {
		return ResultsOutput{}, err
	}
	// Close the race with a concurrent revoke, archive or edit.
	again, lvl, err := loadView(ctx, s.pool, id, vw, false)
	if err != nil {
		return ResultsOutput{}, err
	}
	if _, err := s.access(ctx, c, again, lvl); err != nil || !canRun(lvl) {
		return ResultsOutput{}, ErrNotFound
	}
	if again.ArchivedAt != nil {
		return ResultsOutput{}, ErrNotFound
	}
	if again.Version != v.Version {
		return ResultsOutput{}, ErrConflict
	}
	out.Warnings = append(warnings, out.Warnings...)
	return ResultsOutput{View: ref, Result: out}, nil
}
