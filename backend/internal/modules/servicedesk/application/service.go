package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/relationships"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// Service performs Service Desk operations. Audit actions: servicedesk.ticket.created,
// .assigned, .priority_changed, .comment_added and one per lifecycle operation. Titles,
// descriptions and comment bodies are never copied into audit; ids, statuses and given
// reasons are.
type Service struct {
	store   Store
	dir     Directory
	device  Device
	engine  *query.Engine
	queues  QueueStore
	members Memberships
	// changes, graph and graphDB link Tickets to Changes (WithChanges); nil without them.
	changes ChangeReader
	graph   *relationships.Graph
	graphDB relationships.Querier
}

// NewService builds the service. A store that also implements QueueStore enables Queues; without it every Ticket
// goes to the intake Queue chosen by the database.
func NewService(store Store, dir Directory, device Device) *Service {
	s := &Service{store: store, dir: dir, device: device, engine: query.NewEphemeralEngine()}
	if qs, ok := store.(QueueStore); ok {
		s.queues = qs
	}
	return s
}

func publish(ctx context.Context, tx pgx.Tx, c Caller, typ string, payload map[string]any) error {
	var actor *string
	if c.Actor.UserID != "" {
		a := c.Actor.UserID
		actor = &a
	}
	return events.Publish(ctx, tx, events.Publication{Type: typ, ActorID: actor, CorrelationID: c.CorrelationID, Payload: payload})
}

func state(t *Ticket) any {
	if t == nil {
		return nil
	}
	return map[string]any{"status": t.Status, "priority": t.Priority, "assignee": t.AssigneeID, "queue": t.QueueTeamID, "queueId": t.QueueID, "waitingReason": t.WaitingReason, "version": t.Version}
}

func record(ctx context.Context, tx pgx.Tx, c Caller, action string, before, after *Ticket, meta map[string]any) error {
	id := ""
	if after != nil {
		id = after.ID
	} else {
		id = before.ID
	}
	if len(meta) == 0 {
		meta = nil
	}
	return audit.Record(ctx, tx, audit.Change{
		Action: action, TargetType: "ticket", TargetID: id, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Before: state(before), After: state(after), Metadata: meta,
	})
}

func cleanText(s string, limit int, required bool, what string) (string, error) {
	s = strings.TrimSpace(s)
	if (required && s == "") || utf8.RuneCountInString(s) > limit || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, what == "description" || what == "comment" || what == "resolution") {
		return "", invalid("%s must be at most %d characters without control or invisible formatting characters%s", what, limit, map[bool]string{true: " and must not be empty", false: ""}[required])
	}
	return s, nil
}

func samePtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *Service) isOwner(t Ticket, p Principal) bool {
	return p.UserID != "" && (t.ReporterID == p.UserID || t.AffectedUserID == p.UserID)
}

// ---- create ----

// CreateInput describes a new ticket. Only the title is required. AffectedUserID, Priority and QueueTeamID need
// work access in the Queue; everyone else reports for themselves. QueueID or QueueKey choose the Queue (the
// caller needs the create level in it); without either the intake Queue is used.
type CreateInput struct {
	Title          string
	Description    string
	AffectedUserID *string
	AssetID        *string
	Priority       string
	QueueTeamID    *string
	QueueID        *string
	QueueKey       string
	// PatientImpact is the reporter's signal that patient care is affected. For an employee it raises the priority to
	// high (PatientImpactPriority) when nothing higher applies; urgent stays a staff decision. Staff who set a
	// priority explicitly keep their choice; the flag is stored either way.
	PatientImpact bool
	// Impact is the impact the reporter chose in the report form: one of ReportImpacts. Only patient_care (the same as
	// PatientImpact) changes the priority; the others are stored as a signal for triage.
	Impact string
}

// ReportImpacts are the impact choices of the report form.
var ReportImpacts = []string{"patient_care", "blocked", "impaired", "request"}

// PatientImpactPriority is the priority a patient-impact report gets: the highest an employee can cause.
const PatientImpactPriority = "high"

// intakeQueue resolves the Queue a new Ticket goes into and checks the same create grant the UI list applies.
// An unknown and a forbidden Queue are the same answer, so a Queue id cannot be probed.
func (s *Service) intakeQueue(a access, in CreateInput) (Queue, error) {
	if s.queues == nil {
		return Queue{}, nil
	}
	var q Queue
	chosen := false
	switch {
	case in.QueueID != nil && *in.QueueID != "":
		chosen = true
		r, ok := a.queues[strings.ToLower(*in.QueueID)]
		if !ok {
			return Queue{}, ErrQueueNotPermitted
		}
		q = r.Queue
	case in.QueueKey != "":
		chosen = true
		found := false
		for _, r := range a.queues {
			if r.Queue.Key == in.QueueKey {
				q, found = r.Queue, true
				break
			}
		}
		if !found {
			return Queue{}, ErrQueueNotPermitted
		}
	default:
		found := false
		for _, r := range a.queues {
			if r.Queue.DefaultIntake {
				q, found = r.Queue, true
				break
			}
		}
		if !found {
			return Queue{}, ErrQueueNotPermitted
		}
	}
	if q.Status == QueueArchived {
		if a.canView(q.ID) {
			return Queue{}, ErrQueueArchived
		}
		return Queue{}, ErrQueueNotPermitted
	}
	if !a.canCreate(q.ID) || (chosen && q.RoutingMode == RoutingAutomatic && !a.canView(q.ID)) {
		return Queue{}, ErrQueueNotPermitted
	}
	return q, nil
}

// priorityRank orders priorities: higher is more urgent.
func priorityRank(p string) int {
	switch p {
	case "urgent":
		return 3
	case "high":
		return 2
	case "normal":
		return 1
	}
	return 0
}

func isUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// Create raises a ticket. Every signed-in User may into a Queue they may raise tickets in; the device must be one
// the affected User currently holds (staff may pick any device).
func (s *Service) Create(ctx context.Context, c Caller, p Principal, in CreateInput) (Ticket, error) {
	if err := c.validate(); err != nil {
		return Ticket{}, err
	}
	if p.UserID == "" {
		return Ticket{}, ErrForbidden
	}
	title, err := cleanText(in.Title, maxTitle, true, "title")
	if err != nil {
		return Ticket{}, err
	}
	desc, err := cleanText(in.Description, maxText, false, "description")
	if err != nil {
		return Ticket{}, err
	}
	a, err := s.resolve(ctx, p)
	if err != nil {
		return Ticket{}, err
	}
	queue, err := s.intakeQueue(a, in)
	if err != nil {
		return Ticket{}, err
	}
	eff := a.eff(p, queue.ID)
	t := Ticket{Kind: "incident", Title: title, Description: strPtr(desc), Status: StatusNew, Priority: "normal", ReporterID: p.UserID, AffectedUserID: p.UserID, QueueID: queue.ID}
	if queue.DefaultPriority != "" && in.Priority == "" {
		t.Priority = queue.DefaultPriority
	}
	if in.AffectedUserID != nil && !strings.EqualFold(*in.AffectedUserID, p.UserID) {
		target := strings.ToLower(*in.AffectedUserID)
		if !eff.Manage {
			// Any employee may raise a ticket for a colleague (the people lookup finds them). Both must be active
			// internal employees: external accounts neither raise tickets for others nor can be named.
			ed, ok := s.dir.(EmployeeDirectory)
			if !ok {
				return Ticket{}, ErrForbidden
			}
			emp, err := ed.ActiveEmployees(ctx, []string{p.UserID, target})
			if err != nil {
				return Ticket{}, fmt.Errorf("check employees: %w", err)
			}
			if !emp[p.UserID] {
				return Ticket{}, ErrForbidden
			}
			if !emp[target] {
				return Ticket{}, ErrUserInvalid
			}
		}
		t.AffectedUserID = target
	}
	if in.Impact != "" {
		if !slices.Contains(ReportImpacts, in.Impact) {
			return Ticket{}, invalid("impact must be one of %s", strings.Join(ReportImpacts, ", "))
		}
		t.ReportedImpact = in.Impact
		in.PatientImpact = in.PatientImpact || in.Impact == "patient_care"
	}
	if in.PatientImpact {
		t.PatientImpact = true
		if in.Priority == "" && priorityRank(t.Priority) < priorityRank(PatientImpactPriority) {
			t.Priority = PatientImpactPriority
		}
	}
	if in.Priority != "" && in.Priority != "normal" {
		if !eff.Manage {
			return Ticket{}, ErrForbidden
		}
		if !slices.Contains(Priorities, in.Priority) {
			return Ticket{}, invalid("priority must be one of %s", strings.Join(Priorities, ", "))
		}
		t.Priority = in.Priority
	}
	if in.QueueTeamID != nil {
		if !eff.Manage {
			return Ticket{}, ErrForbidden
		}
		ok, err := s.dir.ActiveTeams(ctx, []string{*in.QueueTeamID})
		if err != nil {
			return Ticket{}, fmt.Errorf("check queue: %w", err)
		}
		if !ok[*in.QueueTeamID] {
			return Ticket{}, ErrTeamInvalid
		}
		t.QueueTeamID = in.QueueTeamID
	} else if queue.DefaultTeamID != nil {
		// The desk's default Team is only a routing hint; a deactivated Team is ignored.
		if ok, err := s.dir.ActiveTeams(ctx, []string{*queue.DefaultTeamID}); err == nil && ok[*queue.DefaultTeamID] {
			t.QueueTeamID = queue.DefaultTeamID
		}
	}
	active, err := s.dir.ActiveUsers(ctx, []string{t.ReporterID, t.AffectedUserID})
	if err != nil {
		return Ticket{}, fmt.Errorf("check users: %w", err)
	}
	if !active[t.ReporterID] || !active[t.AffectedUserID] {
		return Ticket{}, ErrUserInvalid
	}
	if in.AssetID != nil {
		// Everybody, staff included, can only attach a device the affected User holds: ticket
		// handling must not become a way to read arbitrary assets.
		snap, err := s.device.Snapshot(ctx, *in.AssetID, t.AffectedUserID)
		if err != nil {
			return Ticket{}, ErrDeviceInvalid
		}
		t.AssetID, t.DeviceSnapshot = in.AssetID, snap
	}
	if ld, ok := s.dir.(interface {
		PrimaryLocationIDs(ctx context.Context, ids []string) (map[string]string, error)
	}); ok {
		locs, err := ld.PrimaryLocationIDs(ctx, []string{t.AffectedUserID})
		if err != nil {
			return Ticket{}, fmt.Errorf("load location: %w", err)
		}
		if loc, ok := locs[t.AffectedUserID]; ok {
			t.AffectedLocationID = &loc
		}
	}
	var out Ticket
	// The number comes from the Queue counter inside the insert; a unique violation (a safety net that the counter
	// lock should make unreachable) is retried once.
	for attempt := 0; attempt < 2; attempt++ {
		err = s.store.InTx(ctx, func(tx pgx.Tx) error {
			out, err = s.store.InsertTx(ctx, tx, t)
			if err != nil {
				return err
			}
			meta := map[string]any{"affectedUserId": out.AffectedUserID, "queueId": out.QueueID, "onBehalf": out.AffectedUserID != out.ReporterID, "patientImpact": out.PatientImpact}
			if out.AssetID != nil {
				meta["assetId"] = *out.AssetID
			}
			if err := record(ctx, tx, c, "servicedesk.ticket.created", nil, &out, meta); err != nil {
				return err
			}
			return publish(ctx, tx, c, "TicketCreated", map[string]any{"ticketId": out.ID, "reporterId": out.ReporterID, "affectedUserId": out.AffectedUserID, "queueId": out.QueueID})
		})
		if !isUniqueViolation(err) {
			break
		}
	}
	if err != nil {
		return Ticket{}, err
	}
	if err := s.shape(ctx, a, &out); err != nil {
		return Ticket{}, err
	}
	return out, nil
}

// ---- operations ----

// work locks the Ticket and authorizes the caller against its Queue in the same transaction: the Ticket row is
// locked first, then the grants are read. A Ticket the caller may not see is ErrNotFound (the same answer as for an
// unknown id). The returned Principal is the caller's authority over this Ticket.
func (s *Service) work(ctx context.Context, tx pgx.Tx, p Principal, m memberships, id string) (Ticket, access, Principal, error) {
	cur, err := s.store.LockTx(ctx, tx, id)
	if err != nil {
		return Ticket{}, access{}, p, err
	}
	a, err := s.resolveTx(ctx, tx, p, m)
	if err != nil {
		return Ticket{}, access{}, p, err
	}
	eff := a.eff(p, cur.QueueID)
	if !eff.staff() && !s.isOwner(cur, p) {
		return Ticket{}, access{}, p, ErrNotFound
	}
	return cur, a, eff, nil
}

// Params are the inputs of a lifecycle operation.
type Params struct {
	// Reason is the waiting reason code for wait, the resolution text for resolve and the
	// reason for reopen and cancel.
	Reason string
}

// Transition performs a lifecycle operation. People who work the Ticket's Queue (tickets.manage or a work grant)
// may do all of them; the reporter and the affected User may close, reopen and (before work started) cancel.
// Unknown and foreign tickets are 404 for people without access.
func (s *Service) Transition(ctx context.Context, c Caller, p Principal, id string, expected *int, op string, params Params) (Ticket, error) {
	if err := c.validate(); err != nil {
		return Ticket{}, err
	}
	r, ok := rules[op]
	if !ok {
		return Ticket{}, invalid("unknown operation %q", op)
	}
	text := ""
	switch {
	case op == OpWait:
		if !slices.Contains(WaitingReasons, params.Reason) {
			return Ticket{}, invalid("waiting reason must be one of %s", strings.Join(WaitingReasons, ", "))
		}
		text = params.Reason
	case op == OpResolve:
		t, err := cleanText(params.Reason, maxText, true, "resolution")
		if err != nil {
			return Ticket{}, err
		}
		text = t
	case r.reasonRequired:
		t, err := cleanText(params.Reason, maxReason, true, "reason")
		if err != nil {
			return Ticket{}, err
		}
		text = t
	}
	m, err := s.memberships(ctx, p.UserID)
	if err != nil {
		return Ticket{}, err
	}
	var out Ticket
	var acc access
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, a, ep, err := s.work(ctx, tx, p, m, id)
		if err != nil {
			return err
		}
		acc = a
		if expected != nil && *expected != cur.Version {
			return ErrVersionConflict
		}
		if !slices.Contains(AllowedOperations(cur, ep), op) {
			if !ep.Manage && !(s.isOwner(cur, p) && r.ownerMay) {
				return ErrForbidden
			}
			return &InvalidTransitionError{Operation: op, From: cur.Status}
		}
		next := cur
		next.Status, next.WaitingReason, next.StatusReason = r.to, nil, nil
		now := time.Now().UTC()
		switch op {
		case OpWait:
			next.WaitingReason = &text
		case OpResolve:
			next.Resolution, next.ResolvedAt = &text, &now
		case OpClose:
			next.ClosedAt = &now
		case OpReopen:
			next.StatusReason, next.ResolvedAt, next.ClosedAt = &text, nil, nil
		case OpCancel:
			next.StatusReason = &text
		}
		if (op == OpStart || op == OpResume) && next.AssigneeID == nil && c.Actor.UserID != "" {
			me := c.Actor.UserID
			next.AssigneeID = &me
		}
		out, err = s.store.UpdateTx(ctx, tx, next)
		if err != nil {
			return err
		}
		meta := map[string]any{"operation": op}
		if op == OpWait || op == OpReopen || op == OpCancel {
			meta["reason"] = text
		}
		if err := record(ctx, tx, c, "servicedesk.ticket."+op, &cur, &out, meta); err != nil {
			return err
		}
		if op == OpResolve {
			return publish(ctx, tx, c, "TicketResolved", map[string]any{"ticketId": out.ID, "reporterId": out.ReporterID, "affectedUserId": out.AffectedUserID})
		}
		if op == OpReopen || op == OpClose || op == OpCancel {
			return publish(ctx, tx, c, "TicketStatusChanged", map[string]any{"ticketId": out.ID, "operation": op})
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	return out, s.shape(ctx, acc, &out)
}

// assigneeMaySee reports whether the User keeps access to Tickets of the Queue: through the global permissions or
// a grant. Without a membership source (tests) every assignee passes.
func (s *Service) assigneeMaySee(ctx context.Context, tx pgx.Tx, userID, queueID string) (bool, error) {
	if s.members == nil || s.queues == nil {
		return true, nil
	}
	perms, err := s.members.Permissions(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("load permissions of assignee: %w", err)
	}
	_, view := perms[permTicketsView]
	_, manage := perms[permTicketsManage]
	if view || manage {
		return true, nil
	}
	m, err := s.memberships(ctx, userID)
	if err != nil {
		return false, err
	}
	rows, err := s.queues.QueueAccessTx(ctx, tx, userID, m.teams, m.roles)
	if err != nil {
		return false, fmt.Errorf("load queue access of assignee: %w", err)
	}
	for _, r := range rows {
		if r.Queue.ID == queueID {
			return r.Level >= levelView, nil
		}
	}
	return false, nil
}

// Assign sets the assignee and/or the routing Team (nil leaves a field, an empty string clears it). A new ticket
// that gets an assignee becomes open. Requires work access in the Ticket's Queue (tickets.manage or a work grant);
// the assignee must be able to view the Queue. The routing Team is a hint of who handles the Ticket, not a Queue:
// moving a Ticket between desks is MoveToQueue.
func (s *Service) Assign(ctx context.Context, c Caller, p Principal, id string, expected *int, assigneeID, queueTeamID *string) (Ticket, error) {
	return s.AssignWithReason(ctx, c, p, id, expected, assigneeID, queueTeamID, "")
}

// AssignWithReason is Assign with an optional short reason (shown in the Ticket history, copied into the audit
// event like the reasons of the lifecycle operations).
func (s *Service) AssignWithReason(ctx context.Context, c Caller, p Principal, id string, expected *int, assigneeID, queueTeamID *string, reason string) (Ticket, error) {
	if err := c.validate(); err != nil {
		return Ticket{}, err
	}
	reason, err := cleanText(reason, maxReason, false, "reason")
	if err != nil {
		return Ticket{}, err
	}
	a0, err := s.resolve(ctx, p)
	if err != nil {
		return Ticket{}, err
	}
	if !a0.anyWork() {
		return Ticket{}, ErrForbidden
	}
	if assigneeID != nil && *assigneeID != "" {
		ok, err := s.dir.ActiveUsers(ctx, []string{*assigneeID})
		if err != nil {
			return Ticket{}, fmt.Errorf("check assignee: %w", err)
		}
		if !ok[*assigneeID] {
			return Ticket{}, ErrUserInvalid
		}
	}
	if queueTeamID != nil && *queueTeamID != "" {
		ok, err := s.dir.ActiveTeams(ctx, []string{*queueTeamID})
		if err != nil {
			return Ticket{}, fmt.Errorf("check queue: %w", err)
		}
		if !ok[*queueTeamID] {
			return Ticket{}, ErrTeamInvalid
		}
	}
	m, err := s.memberships(ctx, p.UserID)
	if err != nil {
		return Ticket{}, err
	}
	var out Ticket
	var acc access
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, a, ep, err := s.work(ctx, tx, p, m, id)
		if err != nil {
			return err
		}
		acc = a
		if !ep.Manage {
			return ErrForbidden
		}
		if expected != nil && *expected != cur.Version {
			return ErrVersionConflict
		}
		if slices.Contains([]string{StatusResolved, StatusClosed, StatusCancelled}, cur.Status) {
			return &InvalidTransitionError{Operation: "assign", From: cur.Status}
		}
		next := cur
		if assigneeID != nil {
			next.AssigneeID = strPtr(*assigneeID)
		}
		if queueTeamID != nil {
			next.QueueTeamID = strPtr(*queueTeamID)
		}
		if next.AssigneeID != nil && !samePtr(next.AssigneeID, cur.AssigneeID) {
			ok, err := s.assigneeMaySee(ctx, tx, *next.AssigneeID, cur.QueueID)
			if err != nil {
				return err
			}
			if !ok {
				return ErrAssigneeNoAccess
			}
		}
		if next.AssigneeID != nil && next.Status == StatusNew {
			next.Status = StatusOpen
		}
		if samePtr(next.AssigneeID, cur.AssigneeID) && samePtr(next.QueueTeamID, cur.QueueTeamID) && next.Status == cur.Status {
			out = cur
			return nil
		}
		out, err = s.store.UpdateTx(ctx, tx, next)
		if err != nil {
			return err
		}
		var meta map[string]any
		if reason != "" {
			meta = map[string]any{"reason": reason}
		}
		if err := record(ctx, tx, c, "servicedesk.ticket.assigned", &cur, &out, meta); err != nil {
			return err
		}
		if out.AssigneeID != nil && !samePtr(cur.AssigneeID, out.AssigneeID) {
			return publish(ctx, tx, c, "TicketAssigned", map[string]any{"ticketId": out.ID, "assigneeId": *out.AssigneeID})
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	return out, s.shape(ctx, acc, &out)
}

// anyWork reports whether the caller works at least one Queue.
func (a access) anyWork() bool {
	if a.allWork {
		return true
	}
	for _, r := range a.queues {
		if r.Level >= levelWork {
			return true
		}
	}
	return false
}

// SetPriority changes the priority. Requires work access in the Ticket's Queue.
func (s *Service) SetPriority(ctx context.Context, c Caller, p Principal, id string, expected *int, priority string) (Ticket, error) {
	if err := c.validate(); err != nil {
		return Ticket{}, err
	}
	a0, err := s.resolve(ctx, p)
	if err != nil {
		return Ticket{}, err
	}
	if !a0.anyWork() {
		return Ticket{}, ErrForbidden
	}
	if !slices.Contains(Priorities, priority) {
		return Ticket{}, invalid("priority must be one of %s", strings.Join(Priorities, ", "))
	}
	m, err := s.memberships(ctx, p.UserID)
	if err != nil {
		return Ticket{}, err
	}
	var out Ticket
	var acc access
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, a, ep, err := s.work(ctx, tx, p, m, id)
		if err != nil {
			return err
		}
		acc = a
		if !ep.Manage {
			return ErrForbidden
		}
		if expected != nil && *expected != cur.Version {
			return ErrVersionConflict
		}
		if cur.Priority == priority {
			out = cur
			return nil
		}
		next := cur
		next.Priority = priority
		out, err = s.store.UpdateTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return record(ctx, tx, c, "servicedesk.ticket.priority_changed", &cur, &out, nil)
	})
	if err != nil {
		return out, err
	}
	return out, s.shape(ctx, acc, &out)
}

// AddComment adds a comment. The reporter and the affected User add public comments (a comment on a ticket waiting
// for the customer resumes it); people who work the Queue add public or internal ones; view access alone cannot
// comment.
func (s *Service) AddComment(ctx context.Context, c Caller, p Principal, id, body string, internal bool) (Comment, error) {
	return s.AddCommentWithMentions(ctx, c, p, id, body, internal, nil)
}

// MaxMentions is how many people one internal note can mention.
const MaxMentions = 10

// AddCommentWithMentions is AddComment with the people an internal note mentions. Mentions are only allowed on
// internal notes. Mentioned people who are inactive or cannot view the Ticket's Queue are dropped (a mention must
// not disclose a Ticket); the stored list holds the rest and drives the ticket.mention notification.
func (s *Service) AddCommentWithMentions(ctx context.Context, c Caller, p Principal, id, body string, internal bool, mentions []string) (Comment, error) {
	if err := c.validate(); err != nil {
		return Comment{}, err
	}
	b, err := cleanText(body, maxText, true, "comment")
	if err != nil {
		return Comment{}, err
	}
	mentioned, err := cleanMentions(mentions, internal)
	if err != nil {
		return Comment{}, err
	}
	m, err := s.memberships(ctx, p.UserID)
	if err != nil {
		return Comment{}, err
	}
	var out Comment
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, _, ep, err := s.work(ctx, tx, p, m, id)
		if err != nil {
			return err
		}
		owner := s.isOwner(cur, p)
		if !ep.Manage && !owner {
			return ErrForbidden
		}
		if internal && !ep.Manage {
			return ErrForbidden
		}
		if cur.Status == StatusCancelled || cur.Status == StatusClosed {
			return &InvalidTransitionError{Operation: "comment", From: cur.Status}
		}
		mentioned, err = s.mentionable(ctx, tx, mentioned, p.UserID, cur.QueueID)
		if err != nil {
			return err
		}
		out, err = s.store.InsertCommentTx(ctx, tx, Comment{TicketID: cur.ID, AuthorID: p.UserID, Body: b, Internal: internal, MentionedUserIDs: mentioned})
		if err != nil {
			return err
		}
		if owner && !ep.Manage && cur.Status == StatusWaiting && cur.WaitingReason != nil && *cur.WaitingReason == "customer" {
			next := cur
			next.Status, next.WaitingReason = StatusInProgress, nil
			after, err := s.store.UpdateTx(ctx, tx, next)
			if err != nil {
				return err
			}
			if err := record(ctx, tx, c, "servicedesk.ticket.resume", &cur, &after, map[string]any{"cause": "customer_replied"}); err != nil {
				return err
			}
		}
		if err := audit.Record(ctx, tx, audit.Change{Action: "servicedesk.ticket.comment_added", TargetType: "ticket", TargetID: cur.ID, Actor: c.Actor,
			CorrelationID: c.CorrelationID, Metadata: map[string]any{"commentId": out.ID, "internal": internal, "mentionCount": len(out.MentionedUserIDs)}}); err != nil {
			return err
		}
		return publish(ctx, tx, c, "TicketCommentAdded", map[string]any{"ticketId": cur.ID, "commentId": out.ID, "internal": internal, "authorId": p.UserID, "mentionedUserIds": out.MentionedUserIDs})
	})
	return out, err
}

// cleanMentions validates the mention list: internal notes only, at most MaxMentions distinct ids.
func cleanMentions(ids []string, internal bool) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if !internal {
		return nil, invalid("only internal notes can mention people")
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.ToLower(strings.TrimSpace(id))
		if !uuidPattern.MatchString(id) {
			return nil, invalid("mentionedUserIds must be user ids")
		}
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	if len(out) > MaxMentions {
		return nil, invalid("a note can mention at most %d people", MaxMentions)
	}
	return out, nil
}

// mentionable keeps the mentioned people who are active and may view the Queue; the author is dropped.
func (s *Service) mentionable(ctx context.Context, tx pgx.Tx, ids []string, authorID, queueID string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	active, err := s.dir.ActiveUsers(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("check mentioned users: %w", err)
	}
	out := []string{}
	for _, id := range ids {
		if id == authorID || !active[id] {
			continue
		}
		ok, err := s.assigneeMaySee(ctx, tx, id, queueID)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, id)
		}
	}
	return out, nil
}

// SetLocation sets the affected Location of the Ticket (the place the problem is at, which the reporter's profile
// only suggests at creation). A nil locationID clears it. Requires work access in the Ticket's Queue; the Location
// must exist and be active. Version-guarded, audited and part of the Ticket history.
func (s *Service) SetLocation(ctx context.Context, c Caller, p Principal, id string, expected *int, locationID *string) (Ticket, error) {
	if err := c.validate(); err != nil {
		return Ticket{}, err
	}
	if err := needVersion(expected); err != nil {
		return Ticket{}, err
	}
	var loc *string
	if locationID != nil {
		l := strings.ToLower(strings.TrimSpace(*locationID))
		if !uuidPattern.MatchString(l) {
			return Ticket{}, invalid("locationId must be a location id")
		}
		ld, ok := s.dir.(interface {
			ActiveLocations(ctx context.Context, ids []string) (map[string]bool, error)
		})
		if !ok {
			return Ticket{}, invalid("locations are not available")
		}
		active, err := ld.ActiveLocations(ctx, []string{l})
		if err != nil {
			return Ticket{}, fmt.Errorf("check location: %w", err)
		}
		if !active[l] {
			return Ticket{}, invalid("the location does not exist or is not active")
		}
		loc = &l
	}
	m, err := s.memberships(ctx, p.UserID)
	if err != nil {
		return Ticket{}, err
	}
	var out Ticket
	var acc access
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, a, ep, err := s.work(ctx, tx, p, m, id)
		if err != nil {
			return err
		}
		acc = a
		if !AbilitiesOf(cur, ep, s.queues != nil).SetLocation {
			if !ep.Manage {
				return ErrForbidden
			}
			return &InvalidTransitionError{Operation: "set_location", From: cur.Status}
		}
		if *expected != cur.Version {
			return ErrVersionConflict
		}
		if samePtr(cur.AffectedLocationID, loc) {
			out = cur
			return nil
		}
		out, err = s.store.UpdateLocationTx(ctx, tx, cur.ID, loc)
		if err != nil {
			return err
		}
		return record(ctx, tx, c, "servicedesk.ticket.location_changed", &cur, &out, map[string]any{"fromLocationId": deref(cur.AffectedLocationID), "toLocationId": deref(out.AffectedLocationID)})
	})
	if err != nil {
		return out, err
	}
	return out, s.shape(ctx, acc, &out)
}

// MoveReasonValid reports whether the reason code is one of MoveReasons.
func MoveReasonValid(code string) bool { return slices.Contains(MoveReasons, code) }

// MoveToQueue moves a Ticket to another Queue. The Ticket gets the next number of the target Queue and keeps the
// old one as an alias; its id, status, comments, relationships and external references do not change. The
// expected version is required. Authorization is decided in the transaction that moves: the Ticket, the source
// Queue (work access) and the target Queue (create access, or servicedesk.queues.manage) are checked against one
// snapshot, with both Queue rows locked in id order. An assignee who cannot view the target Queue is cleared (an
// in-progress Ticket becomes open) and the routing Team becomes the target's default Team.
func (s *Service) MoveToQueue(ctx context.Context, c Caller, p Principal, id string, expected *int, targetQueueID, reason string) (Ticket, error) {
	if err := c.validate(); err != nil {
		return Ticket{}, err
	}
	if s.queues == nil {
		return Ticket{}, errNoQueueStore
	}
	if err := needVersion(expected); err != nil {
		return Ticket{}, err
	}
	if !MoveReasonValid(reason) {
		return Ticket{}, invalid("reason must be one of %s", strings.Join(MoveReasons, ", "))
	}
	target := strings.ToLower(targetQueueID)
	if !uuidPattern.MatchString(target) {
		return Ticket{}, ErrQueueNotPermitted
	}
	m, err := s.memberships(ctx, p.UserID)
	if err != nil {
		return Ticket{}, err
	}
	var out Ticket
	var acc access
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, a, ep, err := s.work(ctx, tx, p, m, id)
		if err != nil {
			return err
		}
		acc = a
		if *expected != cur.Version {
			return ErrVersionConflict
		}
		if !ep.Manage {
			return ErrForbidden
		}
		if cur.QueueID == target {
			return ErrQueueSame
		}
		if !movableStatus(cur.Status) {
			return &InvalidTransitionError{Operation: "move", From: cur.Status}
		}
		tq, known := a.queues[target]
		if !known {
			return ErrQueueNotPermitted
		}
		if tq.Queue.Status != QueueActive {
			if p.QueuesManage || a.canView(target) {
				return ErrQueueArchived
			}
			return ErrQueueNotPermitted
		}
		if !p.QueuesManage && !a.canCreate(target) {
			return ErrQueueNotPermitted
		}
		locked, err := s.queues.LockQueuesTx(ctx, tx, []string{cur.QueueID, target})
		if err != nil {
			return err
		}
		to, ok := locked[target]
		if !ok {
			return ErrQueueNotPermitted
		}
		if to.Status != QueueActive {
			if p.QueuesManage || a.canView(target) {
				return ErrQueueArchived
			}
			return ErrQueueNotPermitted
		}
		next := cur
		next.QueueID = target
		next.QueueTeamID = nil
		if to.DefaultTeamID != nil {
			if ok, err := s.dir.ActiveTeams(ctx, []string{*to.DefaultTeamID}); err == nil && ok[*to.DefaultTeamID] {
				next.QueueTeamID = to.DefaultTeamID
			}
		}
		cleared := false
		if cur.AssigneeID != nil {
			keep, err := s.assigneeMaySee(ctx, tx, *cur.AssigneeID, target)
			if err != nil {
				return err
			}
			if !keep {
				next.AssigneeID, cleared = nil, true
				if next.Status == StatusInProgress {
					next.Status = StatusOpen
				}
			}
		}
		out, err = s.queues.MoveTx(ctx, tx, next)
		if err != nil {
			return err
		}
		meta := map[string]any{"fromQueueId": cur.QueueID, "toQueueId": target, "oldReference": cur.Reference, "newReference": out.Reference,
			"reasonCode": reason, "assigneeCleared": cleared}
		if err := record(ctx, tx, c, "servicedesk.ticket.queue_moved", &cur, &out, meta); err != nil {
			return err
		}
		return publish(ctx, tx, c, "TicketQueueChanged", map[string]any{"ticketId": out.ID, "fromQueueId": cur.QueueID, "toQueueId": target,
			"oldReference": cur.Reference, "newReference": out.Reference})
	})
	if err != nil {
		return Ticket{}, err
	}
	// The actor may have lost sight of the Ticket's new Queue (a create grant is enough to move into it); the
	// disclosure rules decide what they learn about the new desk.
	return out, s.shape(ctx, acc, &out)
}

var errNoQueueStore = errors.New("servicedesk: store does not support queues")

// ---- reads ----

// Detail is a ticket with the comments the caller may see.
type Detail struct {
	Ticket   Ticket
	Comments []Comment
	Allowed  []string
	// Abilities is what the caller may do with the Ticket (see AbilitiesOf).
	Abilities Abilities
	Names     map[string]string
}

// Get returns a ticket the caller may see (404 otherwise). Internal comments need view access in the Queue.
func (s *Service) Get(ctx context.Context, p Principal, id string) (Detail, error) {
	a, err := s.resolve(ctx, p)
	if err != nil {
		return Detail{}, err
	}
	t, err := s.store.Get(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	ep := a.eff(p, t.QueueID)
	if !ep.staff() && !s.isOwner(t, p) {
		return Detail{}, ErrNotFound
	}
	comments, err := s.store.Comments(ctx, id, ep.staff())
	if err != nil {
		return Detail{}, err
	}
	d := Detail{Ticket: t, Comments: comments, Allowed: AllowedOperations(t, ep), Abilities: AbilitiesOf(t, ep, s.queues != nil)}
	if d.Ticket.Aliases, err = s.aliasesFor(ctx, a, t); err != nil {
		return Detail{}, err
	}
	if err := s.shape(ctx, a, &d.Ticket); err != nil {
		return Detail{}, err
	}
	// The number shown is not also an alias (a requester who may not know the Ticket's new desk keeps the old one).
	d.Ticket.Aliases = slices.DeleteFunc(d.Ticket.Aliases, func(r string) bool { return r == d.Ticket.Reference })
	ids := []string{t.ReporterID, t.AffectedUserID}
	for _, c := range comments {
		ids = append(ids, c.AuthorID)
	}
	if d.Ticket.AssigneeID != nil { // the shaped ticket only: a hidden Queue's assignee is not looked up
		ids = append(ids, *d.Ticket.AssigneeID)
	}
	d.Names, err = s.dir.UserNames(ctx, ids)
	if err != nil {
		return Detail{}, fmt.Errorf("load names: %w", err)
	}
	if d.Ticket.QueueTeamID != nil {
		teams, err := s.dir.TeamNames(ctx, []string{*d.Ticket.QueueTeamID})
		if err != nil {
			return Detail{}, fmt.Errorf("load names: %w", err)
		}
		for k, v := range teams {
			d.Names[k] = v
		}
	}
	return d, nil
}

// RefMatch is the result of a reference lookup.
type RefMatch struct {
	TicketID string
	// Reference is the current reference as the caller may know it.
	Reference string
	// Alias is true when the looked-up reference was an earlier number of the Ticket.
	Alias bool
}

// FindByReference resolves a current number or any alias to the Ticket. The lookup applies the Ticket's current
// visibility: an unknown reference, a malformed one and a Ticket the caller may not see are all ErrNotFound.
func (s *Service) FindByReference(ctx context.Context, p Principal, reference string) (RefMatch, error) {
	if p.UserID == "" {
		return RefMatch{}, ErrForbidden
	}
	ref := strings.ToUpper(strings.TrimSpace(reference))
	if s.queues == nil || !referencePattern.MatchString(ref) {
		return RefMatch{}, ErrNotFound
	}
	id, alias, found, err := s.queues.ResolveReference(ctx, ref)
	if err != nil {
		return RefMatch{}, err
	}
	if !found {
		return RefMatch{}, ErrNotFound
	}
	a, err := s.resolve(ctx, p)
	if err != nil {
		return RefMatch{}, err
	}
	t, err := s.store.Get(ctx, id)
	if err != nil {
		return RefMatch{}, err
	}
	if !a.eff(p, t.QueueID).staff() && !s.isOwner(t, p) {
		return RefMatch{}, ErrNotFound
	}
	// The number must be one the caller may know: a hit on a number issued from a Queue they cannot know would
	// confirm that number (and the Queue behind it) to the owner of the Ticket. Same answer as an unknown number.
	if !a.global() {
		entries, err := s.queues.References(ctx, []string{id})
		if err != nil {
			return RefMatch{}, fmt.Errorf("load references: %w", err)
		}
		known := false
		for _, e := range entries[id] {
			if e.Reference == ref {
				known = a.disclosed(e.QueueID)
				break
			}
		}
		if !known {
			return RefMatch{}, ErrNotFound
		}
	}
	if err := s.shape(ctx, a, &t); err != nil {
		return RefMatch{}, err
	}
	return RefMatch{TicketID: t.ID, Reference: t.Reference, Alias: alias}, nil
}

// List returns tickets: scope "mine" (reported or affected) for everyone, "all" for callers who view Tickets
// beyond their own (tickets.view, tickets.manage or a view grant: then their own and those of the Queues they view).
func (s *Service) List(ctx context.Context, p Principal, all bool, f Filter) (Result, error) {
	if p.UserID == "" {
		return Result{}, ErrForbidden
	}
	if f.Status != "" && !slices.Contains(Statuses, f.Status) {
		return Result{}, invalid("unknown status")
	}
	a, err := s.resolve(ctx, p)
	if err != nil {
		return Result{}, err
	}
	if all {
		if !a.anyView() {
			return Result{}, ErrForbidden
		}
	}
	f.UserID, f.QueueIDs = "", nil
	switch {
	case all && a.global():
	case all:
		f.UserID, f.QueueIDs = p.UserID, a.viewIDs()
	default:
		f.UserID = p.UserID
	}
	if !a.anyView() {
		// Routing is not visible to employees, so it cannot be probed through filters either.
		f.QueueID, f.AssigneeID = "", ""
	} else if !a.global() && (f.QueueID != "" || f.AssigneeID != "") {
		// Routing is only visible for the Tickets of Queues the caller views.
		f.Narrow, f.NarrowQueueIDs = true, a.viewIDs()
	}
	f.Page = f.Page.Normalize()
	res, err := s.store.List(ctx, f)
	if err != nil {
		return Result{}, err
	}
	if err := s.shape(ctx, a, ptrs(res.Items)...); err != nil {
		return Result{}, err
	}
	return res, nil
}

func ptrs(ts []Ticket) []*Ticket {
	out := make([]*Ticket, len(ts))
	for i := range ts {
		out[i] = &ts[i]
	}
	return out
}
