package application

import (
	"context"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// Service performs Security operations. Audit actions are security.advisory.<operation> (created,
// imported, updated, criteria_changed, criteria_normalized, analysis_started, applicable,
// not_applicable, remediation_planned, remediation_started, resolved, archived, matched) and
// security.finding.<operation> (investigating, risk_accepted, false_positive, remediation_planned,
// remediation_started, reopened, remediated, observed_again), written in the mutation's transaction
// with ids, states, counts and reason codes only: advisory titles, summaries, URLs and device names are
// never copied into audit.
type Service struct {
	store           Store
	inventory       Inventory
	now             func() time.Time
	tasks           TaskCreator
	changes         ChangeReader
	graph           *relationships.Graph
	names           AssigneeNames
	relationshipsDB relationships.Querier
}

// NewService wires the Security use cases over the Endpoints inventory contract.
func NewService(store Store, inventory Inventory) *Service {
	return &Service{store: store, inventory: inventory, now: func() time.Time { return time.Now().UTC() }}
}

// WithClock replaces the clock (tests).
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

var (
	uuidPattern   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	sourcePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,39}$`)
)

// SourceManual is the source of advisories entered by hand; imports may not use it.
const SourceManual = "manual"

func publish(ctx context.Context, tx pgx.Tx, c Caller, typ string, payload map[string]any) error {
	var actor *string
	if c.Actor.UserID != "" {
		a := c.Actor.UserID
		actor = &a
	}
	return events.Publish(ctx, tx, events.Publication{Type: typ, ActorID: actor, CorrelationID: c.CorrelationID, Payload: payload})
}

func recordAudit(ctx context.Context, tx pgx.Tx, c Caller, action, targetType, id string, before, after any, meta map[string]any) error {
	if len(meta) == 0 {
		meta = nil
	}
	return audit.Record(ctx, tx, audit.Change{
		Action: action, TargetType: targetType, TargetID: id, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Before: before, After: after, Metadata: meta,
	})
}

func advisoryState(a *Advisory) any {
	if a == nil {
		return nil
	}
	return map[string]any{"status": a.Status, "severity": a.Severity, "criteriaRevision": a.CriteriaRevision, "version": a.Version}
}

func findingState(f *Finding) any {
	if f == nil {
		return nil
	}
	m := map[string]any{"status": f.Status, "confidence": f.Confidence, "version": f.Version}
	if f.RiskReviewBy != nil {
		m["riskReviewBy"] = f.RiskReviewBy.Format(time.DateOnly)
	}
	return m
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func requireVersion(expected *int) (int, error) {
	if expected == nil {
		return 0, invalid("expectedVersion is required")
	}
	return *expected, nil
}

func checkReason(reason string, allowed []string) error {
	if !slices.Contains(allowed, reason) {
		return invalid("reason must be one of %s", strings.Join(allowed, ", "))
	}
	return nil
}

func actorOf(c Caller) (user, system *string) {
	if c.Actor.UserID != "" {
		u := c.Actor.UserID
		return &u, nil
	}
	name := c.Actor.System
	return nil, &name
}

func transition(c Caller, subjectID string, from *string, to, op, reason string) Transition {
	t := Transition{SubjectID: subjectID, FromStatus: from, ToStatus: to, Operation: op, Reason: strPtr(reason), CorrelationID: c.CorrelationID}
	t.ActorUserID, t.ActorSystem = actorOf(c)
	return t
}

// cleanLine validates a single-line text; empty is refused when required.
func cleanLine(field, s string, max int, required bool) (*string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		if required {
			return nil, invalid("%s is required", field)
		}
		return nil, nil
	}
	if utf8.RuneCountInString(s) > max || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, false) {
		return nil, invalid("%s must be at most %d characters without control or invisible formatting characters", field, max)
	}
	return &s, nil
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

// CleanSourceURL validates an advisory link: an absolute https URL with a host, without credentials or
// whitespace, at most 2000 characters. Empty means none.
func CleanSourceURL(raw string) (*string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	bad := invalid("sourceUrl must be an https URL of at most %d characters", maxURL)
	if len(raw) > maxURL || !utf8.ValidString(raw) || strings.ContainsAny(raw, " \t\r\n\\\"<>`") || safetext.ContainsUnsafe(raw, false) {
		return nil, bad
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" || !strings.HasPrefix(raw, "https://") {
		return nil, bad
	}
	return &raw, nil
}

// checkTime refuses timestamps outside 1990 to 2200.
func checkTime(field string, t *time.Time) (*time.Time, error) {
	if t == nil {
		return nil, nil
	}
	if t.Year() < 1990 || t.Year() > 2200 {
		return nil, invalid("%s must lie between the years 1990 and 2200", field)
	}
	u := t.UTC().Truncate(time.Microsecond)
	return &u, nil
}

// enqueueMatch queues the matching job of an Advisory in the caller's transaction; at most one is
// pending or running per Advisory (a run that sees a newer criteria revision runs again).
func enqueueMatch(ctx context.Context, q jobs.Querier, advisoryID string) error {
	_, _, err := jobs.Enqueue(ctx, q, jobs.EnqueueRequest{Type: MatchJobType, Payload: map[string]any{"advisoryId": advisoryID},
		DedupeKey: MatchJobType + ":" + advisoryID, MaxAttempts: 5})
	return err
}

// lockAdvisory locks an Advisory, checks the version and that the status is one of from.
func (s *Service) lockAdvisory(ctx context.Context, tx pgx.Tx, id string, expected int, op string, from ...string) (Advisory, error) {
	if !uuidPattern.MatchString(id) {
		return Advisory{}, ErrNotFound
	}
	cur, err := s.store.LockAdvisoryTx(ctx, tx, strings.ToLower(id))
	if err != nil {
		return Advisory{}, err
	}
	if expected != cur.Version {
		return Advisory{}, ErrVersionConflict
	}
	if len(from) > 0 && !slices.Contains(from, cur.Status) {
		return Advisory{}, &InvalidTransitionError{Operation: op, From: cur.Status}
	}
	return cur, nil
}

// lockFinding locks a Finding, checks the version and that the status is one of from.
func (s *Service) lockFinding(ctx context.Context, tx pgx.Tx, id string, expected int, op string, from ...string) (Finding, error) {
	if !uuidPattern.MatchString(id) {
		return Finding{}, ErrNotFound
	}
	cur, err := s.store.LockFindingTx(ctx, tx, strings.ToLower(id))
	if err != nil {
		return Finding{}, err
	}
	if expected != cur.Version {
		return Finding{}, ErrVersionConflict
	}
	if !slices.Contains(from, cur.Status) {
		return Finding{}, &InvalidTransitionError{Operation: op, From: cur.Status}
	}
	return cur, nil
}
