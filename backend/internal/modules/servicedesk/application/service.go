package application

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// Service performs Service Desk operations. Audit actions: servicedesk.ticket.created,
// .assigned, .priority_changed, .comment_added and one per lifecycle operation. Titles,
// descriptions and comment bodies are never copied into audit; ids, statuses and given
// reasons are.
type Service struct {
	store  Store
	dir    Directory
	device Device
}

func NewService(store Store, dir Directory, device Device) *Service {
	return &Service{store: store, dir: dir, device: device}
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
	return map[string]any{"status": t.Status, "priority": t.Priority, "assignee": t.AssigneeID, "queue": t.QueueTeamID, "waitingReason": t.WaitingReason, "version": t.Version}
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

// CreateInput describes a new ticket. Only the title is required. AffectedUserID,
// Priority and QueueTeamID need tickets.manage; everyone else reports for themselves.
type CreateInput struct {
	Title          string
	Description    string
	AffectedUserID *string
	AssetID        *string
	Priority       string
	QueueTeamID    *string
}

// Create raises a ticket. Every signed-in User may; the device must be one the
// affected User currently holds (staff may pick any device).
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
	t := Ticket{Kind: "incident", Title: title, Description: strPtr(desc), Status: StatusNew, Priority: "normal", ReporterID: p.UserID, AffectedUserID: p.UserID}
	if in.AffectedUserID != nil && !strings.EqualFold(*in.AffectedUserID, p.UserID) {
		if !p.Manage {
			return Ticket{}, ErrForbidden
		}
		t.AffectedUserID = strings.ToLower(*in.AffectedUserID)
	}
	if in.Priority != "" && in.Priority != "normal" {
		if !p.Manage {
			return Ticket{}, ErrForbidden
		}
		if !slices.Contains(Priorities, in.Priority) {
			return Ticket{}, invalid("priority must be one of %s", strings.Join(Priorities, ", "))
		}
		t.Priority = in.Priority
	}
	if in.QueueTeamID != nil {
		if !p.Manage {
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
	var out Ticket
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		out, err = s.store.InsertTx(ctx, tx, t)
		if err != nil {
			return err
		}
		meta := map[string]any{"affectedUserId": out.AffectedUserID}
		if out.AssetID != nil {
			meta["assetId"] = *out.AssetID
		}
		if err := record(ctx, tx, c, "servicedesk.ticket.created", nil, &out, meta); err != nil {
			return err
		}
		return publish(ctx, tx, c, "TicketCreated", map[string]any{"ticketId": out.ID, "reporterId": out.ReporterID, "affectedUserId": out.AffectedUserID})
	})
	return out, err
}

// ---- operations ----

func (s *Service) locked(ctx context.Context, tx pgx.Tx, id string, expected *int) (Ticket, error) {
	cur, err := s.store.LockTx(ctx, tx, id)
	if err != nil {
		return Ticket{}, err
	}
	if expected != nil && *expected != cur.Version {
		return Ticket{}, ErrVersionConflict
	}
	return cur, nil
}

// Params are the inputs of a lifecycle operation.
type Params struct {
	// Reason is the waiting reason code for wait, the resolution text for resolve and the
	// reason for reopen and cancel.
	Reason string
}

// Transition performs a lifecycle operation. tickets.manage may do all of them; the
// reporter and the affected User may close, reopen and (before work started) cancel.
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
	var out Ticket
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if !p.staff() && !s.isOwner(cur, p) {
			return ErrNotFound
		}
		if expected != nil && *expected != cur.Version {
			return ErrVersionConflict
		}
		if !slices.Contains(AllowedOperations(cur, p), op) {
			if !p.Manage && !(s.isOwner(cur, p) && r.ownerMay) {
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
		if op == OpStart && next.AssigneeID == nil && c.Actor.UserID != "" {
			me := c.Actor.UserID
			next.AssigneeID = &me
		}
		out, err = s.store.UpdateTx(ctx, tx, next)
		if err != nil {
			return err
		}
		if !p.staff() {
			defer func() { out.QueueTeamID = nil }()
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
	return out, err
}

// Assign sets the assignee and/or the queue Team (nil leaves a field, an empty
// string clears it). A new ticket that gets an assignee becomes open. Requires
// tickets.manage.
func (s *Service) Assign(ctx context.Context, c Caller, p Principal, id string, expected *int, assigneeID, queueTeamID *string) (Ticket, error) {
	if err := c.validate(); err != nil {
		return Ticket{}, err
	}
	if !p.Manage {
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
	var out Ticket
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.locked(ctx, tx, id, expected)
		if err != nil {
			return err
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
		if err := record(ctx, tx, c, "servicedesk.ticket.assigned", &cur, &out, nil); err != nil {
			return err
		}
		if out.AssigneeID != nil && !samePtr(cur.AssigneeID, out.AssigneeID) {
			return publish(ctx, tx, c, "TicketAssigned", map[string]any{"ticketId": out.ID, "assigneeId": *out.AssigneeID})
		}
		return nil
	})
	return out, err
}

// SetPriority changes the priority. Requires tickets.manage.
func (s *Service) SetPriority(ctx context.Context, c Caller, p Principal, id string, expected *int, priority string) (Ticket, error) {
	if err := c.validate(); err != nil {
		return Ticket{}, err
	}
	if !p.Manage {
		return Ticket{}, ErrForbidden
	}
	if !slices.Contains(Priorities, priority) {
		return Ticket{}, invalid("priority must be one of %s", strings.Join(Priorities, ", "))
	}
	var out Ticket
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.locked(ctx, tx, id, expected)
		if err != nil {
			return err
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
	return out, err
}

// AddComment adds a comment. The reporter and the affected User add public
// comments (a comment on a ticket waiting for the customer resumes it); people
// with tickets.manage add public or internal ones; tickets.view alone cannot comment.
func (s *Service) AddComment(ctx context.Context, c Caller, p Principal, id, body string, internal bool) (Comment, error) {
	if err := c.validate(); err != nil {
		return Comment{}, err
	}
	b, err := cleanText(body, maxText, true, "comment")
	if err != nil {
		return Comment{}, err
	}
	var out Comment
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockTx(ctx, tx, id)
		if err != nil {
			return err
		}
		owner := s.isOwner(cur, p)
		if !p.staff() && !owner {
			return ErrNotFound
		}
		if !p.Manage && !owner {
			return ErrForbidden
		}
		if internal && !p.Manage {
			return ErrForbidden
		}
		if cur.Status == StatusCancelled || cur.Status == StatusClosed {
			return &InvalidTransitionError{Operation: "comment", From: cur.Status}
		}
		out, err = s.store.InsertCommentTx(ctx, tx, Comment{TicketID: cur.ID, AuthorID: p.UserID, Body: b, Internal: internal})
		if err != nil {
			return err
		}
		if owner && !p.Manage && cur.Status == StatusWaiting && cur.WaitingReason != nil && *cur.WaitingReason == "customer" {
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
			CorrelationID: c.CorrelationID, Metadata: map[string]any{"commentId": out.ID, "internal": internal}}); err != nil {
			return err
		}
		return publish(ctx, tx, c, "TicketCommentAdded", map[string]any{"ticketId": cur.ID, "commentId": out.ID, "internal": internal, "authorId": p.UserID})
	})
	return out, err
}

// ---- reads ----

// Detail is a ticket with the comments the caller may see.
type Detail struct {
	Ticket   Ticket
	Comments []Comment
	Allowed  []string
	Names    map[string]string
}

// Get returns a ticket the caller may see (404 otherwise). Internal comments need tickets.view.
func (s *Service) Get(ctx context.Context, p Principal, id string) (Detail, error) {
	t, err := s.store.Get(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	if !p.staff() && !s.isOwner(t, p) {
		return Detail{}, ErrNotFound
	}
	comments, err := s.store.Comments(ctx, id, p.staff())
	if err != nil {
		return Detail{}, err
	}
	d := Detail{Ticket: t, Comments: comments, Allowed: AllowedOperations(t, p)}
	if !p.staff() {
		// The reporter sees the work state, not the staff's routing.
		d.Ticket.QueueTeamID = nil
	}
	ids := []string{t.ReporterID, t.AffectedUserID}
	for _, c := range comments {
		ids = append(ids, c.AuthorID)
	}
	if t.AssigneeID != nil {
		ids = append(ids, *t.AssigneeID)
	}
	d.Names, err = s.dir.UserNames(ctx, ids)
	if err != nil {
		return Detail{}, fmt.Errorf("load names: %w", err)
	}
	if t.QueueTeamID != nil && p.staff() {
		teams, err := s.dir.TeamNames(ctx, []string{*t.QueueTeamID})
		if err != nil {
			return Detail{}, fmt.Errorf("load names: %w", err)
		}
		for k, v := range teams {
			d.Names[k] = v
		}
	}
	return d, nil
}

// List returns tickets: scope "mine" (reported or affected) for everyone, "all" with tickets.view.
func (s *Service) List(ctx context.Context, p Principal, all bool, f Filter) (Result, error) {
	if p.UserID == "" {
		return Result{}, ErrForbidden
	}
	if f.Status != "" && !slices.Contains(Statuses, f.Status) {
		return Result{}, invalid("unknown status")
	}
	if all {
		if !p.staff() {
			return Result{}, ErrForbidden
		}
	} else {
		f.UserID = p.UserID
		if !p.staff() {
			// Routing is not visible to employees, so it cannot be probed through filters either.
			f.QueueID, f.AssigneeID = "", ""
		}
	}
	f.Page = f.Page.Normalize()
	res, err := s.store.List(ctx, f)
	if err != nil {
		return Result{}, err
	}
	if !p.staff() {
		for i := range res.Items {
			res.Items[i].QueueTeamID = nil
		}
	}
	return res, nil
}
