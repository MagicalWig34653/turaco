package application

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// Service performs Initiative operations. Audit actions are
// planning.initiative.<operation> (created, updated, planning_started,
// proposed, approved, approval_rejected, activated, held, resumed, completed,
// cancelled, item_added, item_removed, milestone_added, milestone_updated,
// milestone_completed, milestone_reopened, milestone_removed), written in the
// mutation's transaction with ids, states and reason codes only: titles, goals
// and milestone titles are never copied into audit.
type Service struct {
	store       Store
	graph       *relationships.Graph
	dir         Directory
	changes     Changes
	tasks       Tasks
	procurement Procurement
	services    Services
	approvals   Approvals
	now         func() time.Time
}

// NewService wires the Planning use cases over the other modules' public contracts.
func NewService(store Store, graph *relationships.Graph, dir Directory, changes Changes, tasks Tasks, procurement Procurement,
	services Services, approvals Approvals) *Service {
	return &Service{store: store, graph: graph, dir: dir, changes: changes, tasks: tasks, procurement: procurement, services: services,
		approvals: approvals, now: func() time.Time { return time.Now().UTC() }}
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

func recordAudit(ctx context.Context, tx pgx.Tx, c Caller, action, initiativeID string, before, after any, meta map[string]any) error {
	if len(meta) == 0 {
		meta = nil
	}
	return audit.Record(ctx, tx, audit.Change{
		Action: "planning.initiative." + action, TargetType: "initiative", TargetID: initiativeID, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Before: before, After: after, Metadata: meta,
	})
}

func initiativeState(i *Initiative) any {
	if i == nil {
		return nil
	}
	return map[string]any{"status": i.Status, "version": i.Version}
}

func oneOf(s string, set []string) bool { return slices.Contains(set, s) }

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

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

// day truncates a date to midnight UTC (dates carry no time of day).
func day(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func dayPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	d := day(*t)
	return &d
}

// checkDate refuses dates outside 2000-01-01 to 2200-12-31.
func checkDate(field string, t *time.Time) error {
	if t == nil {
		return nil
	}
	if t.Year() < 2000 || t.Year() > 2200 {
		return invalid("%s must lie between the years 2000 and 2200", field)
	}
	return nil
}

func requireVersion(expected *int) (int, error) {
	if expected == nil {
		return 0, invalid("expectedVersion is required")
	}
	return *expected, nil
}

// commit writes a changed Initiative, its transition and InitiativeStatusChanged
// event (when the status moved) and its audit entry in the caller's
// transaction. The Initiative must be locked.
func (s *Service) commit(ctx context.Context, tx pgx.Tx, c Caller, cur, next Initiative, op, reason string, meta map[string]any) (Initiative, error) {
	out, err := s.store.UpdateTx(ctx, tx, next)
	if err != nil {
		return Initiative{}, err
	}
	if cur.Status != out.Status {
		if err := s.recordTransition(ctx, tx, c, out.ID, &cur.Status, out.Status, op, reason); err != nil {
			return Initiative{}, err
		}
		payload := map[string]any{"initiativeId": out.ID, "operation": op, "status": out.Status, "previousStatus": cur.Status}
		if reason != "" {
			payload["reason"] = reason
		}
		if err := publish(ctx, tx, c, EventStatusChanged, payload); err != nil {
			return Initiative{}, err
		}
	}
	if reason != "" {
		if meta == nil {
			meta = map[string]any{}
		}
		meta["reason"] = reason
	}
	if err := recordAudit(ctx, tx, c, op, out.ID, initiativeState(&cur), initiativeState(&out), meta); err != nil {
		return Initiative{}, err
	}
	return out, nil
}

func (s *Service) recordTransition(ctx context.Context, tx pgx.Tx, c Caller, id string, from *string, to, op, reason string) error {
	t := Transition{InitiativeID: id, FromStatus: from, ToStatus: to, Operation: op, Reason: strPtr(reason), CorrelationID: c.CorrelationID}
	if c.Actor.UserID != "" {
		t.ActorUserID = &c.Actor.UserID
	} else {
		name := c.Actor.System
		t.ActorSystem = &name
	}
	return s.store.InsertTransitionTx(ctx, tx, t)
}

// lockStatus locks an Initiative, checks the version and that the status is one of from.
func (s *Service) lockStatus(ctx context.Context, tx pgx.Tx, id string, expected int, op string, from ...string) (Initiative, error) {
	if !uuidPattern.MatchString(id) {
		return Initiative{}, ErrNotFound
	}
	cur, err := s.store.LockTx(ctx, tx, strings.ToLower(id))
	if err != nil {
		return Initiative{}, err
	}
	if expected != cur.Version {
		return Initiative{}, ErrVersionConflict
	}
	if !slices.Contains(from, cur.Status) {
		return Initiative{}, &InvalidTransitionError{Operation: op, From: cur.Status}
	}
	return cur, nil
}

func addEditor(i Initiative, userID string) Initiative {
	if userID != "" && !slices.Contains(i.Editors, userID) {
		i.Editors = append(slices.Clone(i.Editors), userID)
	}
	return i
}

// checkOwner validates an owner: an active User.
func (s *Service) checkOwner(ctx context.Context, id string) (string, error) {
	id, err := checkID(id)
	if err != nil {
		return "", err
	}
	ok, err := s.dir.ActiveUsers(ctx, []string{id})
	if err != nil {
		return "", fmt.Errorf("check owner: %w", err)
	}
	if !ok[id] {
		return "", ErrReferenceInvalid
	}
	return id, nil
}

// NewInitiative describes an Initiative to create. An empty OwnerUserID makes the caller the owner.
type NewInitiative struct {
	Title       string
	Goal        string
	OwnerUserID string
	TargetDate  *time.Time
}

// Create creates an Initiative in status idea. Requires planning.manage.
func (s *Service) Create(ctx context.Context, c Caller, p Principal, in NewInitiative) (Initiative, error) {
	if err := c.validate(); err != nil {
		return Initiative{}, err
	}
	if !p.Manage || p.UserID == "" || p.UserID != c.Actor.UserID {
		return Initiative{}, ErrForbidden
	}
	title, err := cleanTitle(in.Title)
	if err != nil {
		return Initiative{}, err
	}
	goal, err := cleanText("goal", in.Goal, maxGoal)
	if err != nil {
		return Initiative{}, err
	}
	target := dayPtr(in.TargetDate)
	if err := checkDate("targetDate", target); err != nil {
		return Initiative{}, err
	}
	if in.OwnerUserID == "" {
		in.OwnerUserID = p.UserID
	}
	owner, err := s.checkOwner(ctx, in.OwnerUserID)
	if err != nil {
		return Initiative{}, err
	}
	ini := Initiative{Title: title, Goal: goal, OwnerID: owner, Status: StatusIdea, TargetDate: target, CreatedBy: p.UserID}
	var out Initiative
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		out, err = s.store.InsertTx(ctx, tx, ini)
		if err != nil {
			return err
		}
		if err := s.recordTransition(ctx, tx, c, out.ID, nil, out.Status, "create", ""); err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "created", out.ID, nil, initiativeState(&out), nil)
	})
	return out, err
}

// Details changes an Initiative; nil fields stay unchanged. An empty Goal
// clears it; ClearTargetDate removes the target date.
type Details struct {
	Title           *string
	Goal            *string
	OwnerUserID     *string
	TargetDate      *time.Time
	ClearTargetDate bool
}

// UpdateDetails changes title, goal, owner and target date while the
// Initiative is editable (not while a proposal waits for its decision, never
// after it ended). The editor can never approve it. Requires planning.manage
// and expectedVersion.
func (s *Service) UpdateDetails(ctx context.Context, c Caller, p Principal, id string, expected *int, in Details) (Initiative, error) {
	if err := c.validate(); err != nil {
		return Initiative{}, err
	}
	if !p.Manage {
		return Initiative{}, ErrForbidden
	}
	exp, err := requireVersion(expected)
	if err != nil {
		return Initiative{}, err
	}
	var title string
	if in.Title != nil {
		if title, err = cleanTitle(*in.Title); err != nil {
			return Initiative{}, err
		}
	}
	var goal *string
	if in.Goal != nil {
		if goal, err = cleanText("goal", *in.Goal, maxGoal); err != nil {
			return Initiative{}, err
		}
	}
	if in.TargetDate != nil && in.ClearTargetDate {
		return Initiative{}, invalid("set or clear the target date, not both")
	}
	target := dayPtr(in.TargetDate)
	if err := checkDate("targetDate", target); err != nil {
		return Initiative{}, err
	}
	var owner string
	if in.OwnerUserID != nil {
		if owner, err = s.checkOwner(ctx, *in.OwnerUserID); err != nil {
			return Initiative{}, err
		}
	}
	var out Initiative
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.lockStatus(ctx, tx, id, exp, "update", editableStatuses...)
		if err != nil {
			return err
		}
		next := addEditor(cur, c.Actor.UserID)
		var changed []string
		if in.Title != nil && title != cur.Title {
			next.Title = title
			changed = append(changed, "title")
		}
		if in.Goal != nil {
			next.Goal = goal
			changed = append(changed, "goal")
		}
		if in.OwnerUserID != nil && owner != cur.OwnerID {
			next.OwnerID = owner
			changed = append(changed, "owner")
		}
		if in.TargetDate != nil || in.ClearTargetDate {
			next.TargetDate = target
			changed = append(changed, "targetDate")
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
