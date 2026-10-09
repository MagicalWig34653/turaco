package application

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// Service performs Change operations. Audit actions are
// changes.change.<operation> (created, updated, affected_added, affected_removed,
// submitted, assessed, emergency_approved, approved, approval_rejected, scheduled,
// started, completed, failed, reviewed, closed, cancelled, task_added), written in
// the mutation's transaction with ids, states and reason codes only: titles,
// descriptions, rollback plans, justifications and outcome notes are never
// copied into audit.
type Service struct {
	store     Store
	graph     *relationships.Graph
	dir       Directory
	services  Services
	infra     Infrastructure
	assets    Assets
	approvals Approvals
	tasks     Tasks
	now       func() time.Time

	// impactRunning holds the users with a Change impact walk in progress.
	impactMu      sync.Mutex
	impactRunning map[string]bool
}

// NewService wires the Changes use cases over the other modules' public contracts.
func NewService(store Store, graph *relationships.Graph, dir Directory, services Services, infra Infrastructure, assets Assets,
	approvals Approvals, tasks Tasks) *Service {
	return &Service{store: store, graph: graph, dir: dir, services: services, infra: infra, assets: assets,
		approvals: approvals, tasks: tasks, now: func() time.Time { return time.Now().UTC() }, impactRunning: map[string]bool{}}
}

// WithClock replaces the clock (tests).
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func publish(ctx context.Context, tx pgx.Tx, c Caller, typ string, payload map[string]any) error {
	var actor *string
	if c.Actor.UserID != "" {
		a := c.Actor.UserID
		actor = &a
	}
	return events.Publish(ctx, tx, events.Publication{Type: typ, ActorID: actor, CorrelationID: c.CorrelationID, Payload: payload})
}

func recordAudit(ctx context.Context, tx pgx.Tx, c Caller, action, changeID string, before, after any, meta map[string]any) error {
	if len(meta) == 0 {
		meta = nil
	}
	return audit.Record(ctx, tx, audit.Change{
		Action: "changes.change." + action, TargetType: "change", TargetID: changeID, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Before: before, After: after, Metadata: meta,
	})
}

func changeState(c *Change) any {
	if c == nil {
		return nil
	}
	return map[string]any{"status": c.Status, "kind": c.Kind, "risk": c.Risk, "version": c.Version}
}

func oneOf(s string, set []string) bool { return slices.Contains(set, s) }

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func userPtr(c Caller) *string { return strPtr(c.Actor.UserID) }

func checkID(id string) (string, error) {
	if !uuidPattern.MatchString(id) {
		return "", invalid("ids must be UUIDs")
	}
	return strings.ToLower(id), nil
}

func cleanTitle(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > maxTitle || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, false) {
		return "", invalid("title must be 1-%d characters without control or invisible formatting characters", maxTitle)
	}
	return s, nil
}

// cleanText validates optional multi-line text; empty means none.
func cleanText(field, s string, max int) (*string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(s) > max || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, true) {
		return nil, invalid("%s must be at most %d characters without control or invisible formatting characters", field, max)
	}
	return &s, nil
}

// checkWindow validates a maintenance window: both ends or none, end after
// start, at most 30 days. It does not look at the clock.
func checkWindow(start, end *time.Time) error {
	if (start == nil) != (end == nil) {
		return invalid("a maintenance window needs a start and an end")
	}
	if start == nil {
		return nil
	}
	if !end.After(*start) {
		return invalid("the maintenance window must end after it starts")
	}
	if end.Sub(*start) > MaxWindow {
		return invalid("the maintenance window may last at most 30 days")
	}
	return nil
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC().Truncate(time.Microsecond)
	return &u
}

func requireVersion(expected *int) (int, error) {
	if expected == nil {
		return 0, invalid("expectedVersion is required")
	}
	return *expected, nil
}

// commit writes a changed Change, its transition (when the status moved) and
// its audit entry in the caller's transaction. The Change must be locked.
func (s *Service) commit(ctx context.Context, tx pgx.Tx, c Caller, cur, next Change, op, reason string, meta map[string]any) (Change, error) {
	out, err := s.store.UpdateTx(ctx, tx, next)
	if err != nil {
		return Change{}, err
	}
	if cur.Status != out.Status {
		if err := s.recordTransition(ctx, tx, c, out.ID, &cur.Status, out.Status, op, reason); err != nil {
			return Change{}, err
		}
	}
	if reason != "" {
		if meta == nil {
			meta = map[string]any{}
		}
		meta["reason"] = reason
	}
	if err := recordAudit(ctx, tx, c, op, out.ID, changeState(&cur), changeState(&out), meta); err != nil {
		return Change{}, err
	}
	return out, nil
}

func (s *Service) recordTransition(ctx context.Context, tx pgx.Tx, c Caller, id string, from *string, to, op, reason string) error {
	t := Transition{ChangeID: id, FromStatus: from, ToStatus: to, Operation: op, Reason: strPtr(reason), CorrelationID: c.CorrelationID}
	if c.Actor.UserID != "" {
		t.ActorUserID = &c.Actor.UserID
	} else {
		name := c.Actor.System
		t.ActorSystem = &name
	}
	return s.store.InsertTransitionTx(ctx, tx, t)
}

// lockStatus locks a Change, checks the version and that the status is one of from.
func (s *Service) lockStatus(ctx context.Context, tx pgx.Tx, id string, expected *int, op string, from ...string) (Change, error) {
	if !uuidPattern.MatchString(id) {
		return Change{}, ErrNotFound
	}
	cur, err := s.store.LockTx(ctx, tx, strings.ToLower(id))
	if err != nil {
		return Change{}, err
	}
	if expected != nil && *expected != cur.Version {
		return Change{}, ErrVersionConflict
	}
	if !slices.Contains(from, cur.Status) {
		return Change{}, &InvalidTransitionError{Operation: op, From: cur.Status}
	}
	return cur, nil
}

func addEditor(c Change, userID string) Change {
	if userID != "" && !slices.Contains(c.Editors, userID) {
		c.Editors = append(slices.Clone(c.Editors), userID)
	}
	return c
}

// checkOwner validates an optional owner: an active User.
func (s *Service) checkOwner(ctx context.Context, id string) (*string, error) {
	if id == "" {
		return nil, nil
	}
	id, err := checkID(id)
	if err != nil {
		return nil, err
	}
	ok, err := s.dir.ActiveUsers(ctx, []string{id})
	if err != nil {
		return nil, fmt.Errorf("check owner: %w", err)
	}
	if !ok[id] {
		return nil, ErrReferenceInvalid
	}
	return &id, nil
}

// NewChange describes a Change to create.
type NewChange struct {
	Title        string
	Description  string
	Kind         string
	Risk         string // empty means low
	OwnerUserID  string
	RollbackPlan string
	Window       Window
}

// Create creates a draft Change. The caller is the requester. Requires changes.manage.
func (s *Service) Create(ctx context.Context, c Caller, p Principal, in NewChange) (Change, error) {
	if err := c.validate(); err != nil {
		return Change{}, err
	}
	if !p.Manage || p.UserID == "" || p.UserID != c.Actor.UserID {
		return Change{}, ErrForbidden
	}
	title, err := cleanTitle(in.Title)
	if err != nil {
		return Change{}, err
	}
	if !oneOf(in.Kind, Kinds) {
		return Change{}, invalid("kind must be one of %s", strings.Join(Kinds, ", "))
	}
	if in.Risk == "" {
		in.Risk = RiskLow
	}
	if !oneOf(in.Risk, Risks) {
		return Change{}, invalid("risk must be one of %s", strings.Join(Risks, ", "))
	}
	desc, err := cleanText("description", in.Description, maxDesc)
	if err != nil {
		return Change{}, err
	}
	rollback, err := cleanText("rollback plan", in.RollbackPlan, maxRollback)
	if err != nil {
		return Change{}, err
	}
	ws, we := utc(in.Window.Start), utc(in.Window.End)
	if err := checkWindow(ws, we); err != nil {
		return Change{}, err
	}
	owner, err := s.checkOwner(ctx, in.OwnerUserID)
	if err != nil {
		return Change{}, err
	}
	ch := Change{Title: title, Description: desc, Kind: in.Kind, Risk: in.Risk, Status: StatusDraft, RequesterID: p.UserID,
		OwnerID: owner, RollbackPlan: rollback, WindowStart: ws, WindowEnd: we}
	var out Change
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		out, err = s.store.InsertTx(ctx, tx, ch)
		if err != nil {
			return err
		}
		if err := s.recordTransition(ctx, tx, c, out.ID, nil, out.Status, "create", ""); err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "created", out.ID, nil, changeState(&out), nil)
	})
	return out, err
}

// Details changes a draft or assessed Change; nil fields stay unchanged and an
// empty string clears description, rollback plan and owner. A Window with both
// ends nil clears the window.
type Details struct {
	Title        *string
	Description  *string
	Kind         *string
	Risk         *string
	OwnerUserID  *string
	RollbackPlan *string
	Window       *Window
}

// UpdateDetails changes the details of a Change in draft or assessment. The
// editor can never approve the Change. Requires changes.manage.
func (s *Service) UpdateDetails(ctx context.Context, c Caller, p Principal, id string, expected *int, in Details) (Change, error) {
	if err := c.validate(); err != nil {
		return Change{}, err
	}
	if !p.Manage {
		return Change{}, ErrForbidden
	}
	exp, err := requireVersion(expected)
	if err != nil {
		return Change{}, err
	}
	var title string
	if in.Title != nil {
		if title, err = cleanTitle(*in.Title); err != nil {
			return Change{}, err
		}
	}
	var desc, rollback *string
	if in.Description != nil {
		if desc, err = cleanText("description", *in.Description, maxDesc); err != nil {
			return Change{}, err
		}
	}
	if in.RollbackPlan != nil {
		if rollback, err = cleanText("rollback plan", *in.RollbackPlan, maxRollback); err != nil {
			return Change{}, err
		}
	}
	if in.Kind != nil && !oneOf(*in.Kind, Kinds) {
		return Change{}, invalid("kind must be one of %s", strings.Join(Kinds, ", "))
	}
	if in.Risk != nil && !oneOf(*in.Risk, Risks) {
		return Change{}, invalid("risk must be one of %s", strings.Join(Risks, ", "))
	}
	var ws, we *time.Time
	if in.Window != nil {
		ws, we = utc(in.Window.Start), utc(in.Window.End)
		if err := checkWindow(ws, we); err != nil {
			return Change{}, err
		}
	}
	var owner *string
	if in.OwnerUserID != nil {
		if owner, err = s.checkOwner(ctx, *in.OwnerUserID); err != nil {
			return Change{}, err
		}
	}
	var out Change
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockStatus(ctx, tx, id, &exp, "update", StatusDraft, StatusAssessment)
		if err != nil {
			return err
		}
		next := addEditor(cur, c.Actor.UserID)
		var changed []string
		if in.Title != nil && title != cur.Title {
			next.Title = title
			changed = append(changed, "title")
		}
		if in.Description != nil {
			next.Description = desc
			changed = append(changed, "description")
		}
		if in.Kind != nil && *in.Kind != cur.Kind {
			// The kind decides the approval path; after submission it is fixed.
			if cur.Status != StatusDraft {
				return invalid("the kind can only be changed in draft")
			}
			next.Kind = *in.Kind
			changed = append(changed, "kind")
		}
		if in.Risk != nil && *in.Risk != cur.Risk {
			next.Risk = *in.Risk
			changed = append(changed, "risk")
		}
		if in.OwnerUserID != nil {
			next.OwnerID = owner
			changed = append(changed, "owner")
		}
		if in.RollbackPlan != nil {
			next.RollbackPlan = rollback
			changed = append(changed, "rollbackPlan")
		}
		if in.Window != nil {
			next.WindowStart, next.WindowEnd = ws, we
			changed = append(changed, "window")
		}
		if len(changed) == 0 {
			out = cur
			return nil
		}
		out, err = s.commit(ctx, tx, c, cur, next, "updated", "", map[string]any{"changedFields": changed})
		return err
	})
	return out, err
}

// ---- affected resources ----

// AffectedLink is one affected resource of a Change.
type AffectedLink struct {
	RelationshipID string
	Type           string
	ID             string
	Confidence     string
	Since          time.Time
}

// checkTarget verifies that a resource exists and can be affected and returns
// its normalized id. A resource of a type the caller may not see is
// ErrReferenceInvalid without any lookup, so adding never confirms that hidden
// records exist (the rule of Service dependencies).
func (s *Service) checkTarget(ctx context.Context, p Principal, targetType, targetID string) (string, error) {
	if !oneOf(targetType, AffectedTargets) {
		return "", invalid("type must be one of %s", strings.Join(AffectedTargets, ", "))
	}
	id, err := checkID(targetID)
	if err != nil {
		return "", err
	}
	if p.hides(targetType) {
		return "", ErrReferenceInvalid
	}
	switch targetType {
	case NodeService:
		found, err := s.services.Lookup(ctx, []string{id})
		if err != nil {
			return "", fmt.Errorf("check service: %w", err)
		}
		if v, ok := found[id]; !ok || v.Status == "retired" {
			return "", ErrReferenceInvalid
		}
	case NodeVM:
		found, err := s.infra.VMs(ctx, []string{id})
		if err != nil {
			return "", fmt.Errorf("check virtual machine: %w", err)
		}
		if v, ok := found[id]; !ok || v.State == "decommissioned" {
			return "", ErrReferenceInvalid
		}
	case NodeAsset:
		found, err := s.assets.Assets(ctx, []string{id})
		if err != nil {
			return "", fmt.Errorf("check asset: %w", err)
		}
		if a, ok := found[id]; !ok || !a.Usable() {
			return "", ErrReferenceInvalid
		}
	case NodeLocation:
		ok, err := s.dir.ActiveLocations(ctx, []string{id})
		if err != nil {
			return "", fmt.Errorf("check location: %w", err)
		}
		if !ok[id] {
			return "", ErrReferenceInvalid
		}
	}
	return id, nil
}

// AddAffected records that a draft or assessed Change affects a Service,
// Virtual Machine, Asset or Location. The link is a declared platform
// Relationship (change AFFECTS ...); adding an existing link returns it
// (created is false). At most 25 resources per Change. Requires changes.manage.
func (s *Service) AddAffected(ctx context.Context, c Caller, p Principal, changeID string, expected *int, targetType, targetID string) (l AffectedLink, created bool, err error) {
	if err := c.validate(); err != nil {
		return AffectedLink{}, false, err
	}
	if !p.Manage {
		return AffectedLink{}, false, ErrForbidden
	}
	if !uuidPattern.MatchString(changeID) {
		return AffectedLink{}, false, ErrNotFound
	}
	changeID = strings.ToLower(changeID)
	targetID, err = s.checkTarget(ctx, p, targetType, targetID)
	if err != nil {
		return AffectedLink{}, false, err
	}
	src := relationships.Node{Type: NodeChange, ID: changeID}
	dst := relationships.Node{Type: targetType, ID: targetID}
	var rel relationships.Relationship
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockStatus(ctx, tx, changeID, expected, "add_affected", StatusDraft, StatusAssessment)
		if err != nil {
			return err
		}
		page, err := s.graph.Outgoing(ctx, tx, src, []string{RelAffects}, "", MaxAffected+1)
		if err != nil {
			return fmt.Errorf("count affected resources: %w", err)
		}
		exists := false
		for _, r := range page.Items {
			if r.Target == dst {
				exists = true
			}
		}
		if !exists && len(page.Items) >= MaxAffected {
			return ErrTooMany
		}
		rel, created, err = s.graph.Link(ctx, tx, relationships.LinkInput{
			Owner: RelationshipOwner, Source: src, Type: RelAffects, Target: dst, Confidence: relationships.ConfidenceDeclared,
			CreatedBy: c.Actor.UserID, RecordedBy: "changes",
		})
		if err != nil {
			return fmt.Errorf("link affected resource: %w", err)
		}
		if !created {
			return nil
		}
		next := addEditor(cur, c.Actor.UserID)
		if _, err := s.store.UpdateTx(ctx, tx, next); err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "affected_added", changeID, nil, nil,
			map[string]any{"relationshipId": rel.ID, "targetType": targetType, "targetId": targetID})
	})
	if err != nil {
		return AffectedLink{}, false, err
	}
	return AffectedLink{RelationshipID: rel.ID, Type: targetType, ID: targetID, Confidence: rel.Confidence, Since: rel.ValidFrom}, created, nil
}

// RemoveAffected ends the link between a draft or assessed Change and an
// affected resource. Removing a link that does not exist succeeds without an
// audit entry. A resource of a type the caller may not see is
// ErrReferenceInvalid, as when adding, so removal never confirms hidden links.
// Requires changes.manage and expectedVersion.
func (s *Service) RemoveAffected(ctx context.Context, c Caller, p Principal, changeID string, expected *int, targetType, targetID string) error {
	if err := c.validate(); err != nil {
		return err
	}
	if !p.Manage {
		return ErrForbidden
	}
	exp, err := requireVersion(expected)
	if err != nil {
		return err
	}
	if !uuidPattern.MatchString(changeID) {
		return ErrNotFound
	}
	if !oneOf(targetType, AffectedTargets) {
		return invalid("type must be one of %s", strings.Join(AffectedTargets, ", "))
	}
	targetID, err = checkID(targetID)
	if err != nil {
		return err
	}
	if p.hides(targetType) {
		return ErrReferenceInvalid
	}
	changeID = strings.ToLower(changeID)
	return s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockStatus(ctx, tx, changeID, &exp, "remove_affected", StatusDraft, StatusAssessment)
		if err != nil {
			return err
		}
		ended, err := s.graph.UnlinkTriple(ctx, tx, RelationshipOwner, relationships.Node{Type: NodeChange, ID: changeID}, RelAffects,
			relationships.Node{Type: targetType, ID: targetID}, reasonRemoved, c.Actor.UserID)
		if err != nil {
			return fmt.Errorf("unlink affected resource: %w", err)
		}
		if !ended {
			return nil
		}
		if _, err := s.store.UpdateTx(ctx, tx, addEditor(cur, c.Actor.UserID)); err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "affected_removed", changeID, nil, nil, map[string]any{"targetType": targetType, "targetId": targetID})
	})
}

// checkReady checks what a Change needs before it is submitted and again when
// it is assessed (under the same lock, as details may change in assessment): a
// maintenance window, a rollback plan for medium and high risk and, unless the
// Change is standard, at least one affected resource.
func (s *Service) checkReady(ctx context.Context, tx pgx.Tx, c Change, risk string) error {
	var issues []FieldIssue
	var parts []string
	if c.WindowStart == nil {
		issues = append(issues, FieldIssue{Field: "windowStart", Code: "required"})
		parts = append(parts, "a maintenance window")
	}
	if risk != RiskLow && c.RollbackPlan == nil {
		issues = append(issues, FieldIssue{Field: "rollbackPlan", Code: "required"})
		parts = append(parts, "a rollback plan (medium and high risk)")
	}
	if c.Kind != KindStandard {
		page, err := s.graph.Outgoing(ctx, tx, relationships.Node{Type: NodeChange, ID: c.ID}, []string{RelAffects}, "", 1)
		if err != nil {
			return fmt.Errorf("count affected resources: %w", err)
		}
		if len(page.Items) == 0 {
			issues = append(issues, FieldIssue{Field: "affectedResources", Code: "required"})
			parts = append(parts, "at least one affected resource (service, virtual machine, asset or location)")
		}
	}
	if len(issues) > 0 {
		return &InvalidInputError{Message: "The change is not ready: " + strings.Join(parts, ", ") + " required.", Issues: issues}
	}
	return nil
}

// endLinks ends every current AFFECTS link of a Change that reached a terminal
// status, in the caller's transaction, with the status's end reason. The links
// stay readable by that reason (affected resources of closed Changes, list
// filter), but no longer count as current relationships of the resources.
func (s *Service) endLinks(ctx context.Context, tx pgx.Tx, c Caller, changeID, status string) error {
	if _, err := s.graph.UnlinkAll(ctx, tx, RelationshipOwner, relationships.Node{Type: NodeChange, ID: changeID}, relationships.Forward,
		linkEndReason(status), c.Actor.UserID); err != nil {
		return fmt.Errorf("end affected resource links: %w", err)
	}
	return nil
}

// affectedLinks returns the affected resources of a Change: its current AFFECTS
// links, or for a terminal Change the links ended when it terminated.
func affectedLinks(ctx context.Context, g *relationships.Graph, q relationships.Querier, c Change) ([]relationships.Relationship, error) {
	node := relationships.Node{Type: NodeChange, ID: c.ID}
	if reason := linkEndReason(c.Status); reason != "" {
		return g.OutgoingEnded(ctx, q, node, []string{RelAffects}, reason, MaxAffected)
	}
	page, err := g.Outgoing(ctx, q, node, []string{RelAffects}, "", MaxAffected)
	return page.Items, err
}
