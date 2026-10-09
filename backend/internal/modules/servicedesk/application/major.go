package application

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
)

// Major Incident statuses (docs/domain/state-machines.md).
const (
	MIIdentified    = "identified"
	MIInvestigating = "investigating"
	MIMitigating    = "mitigating"
	MIMonitoring    = "monitoring"
	MIResolved      = "resolved"
	MIClosed        = "closed"
)

// MajorIncident is a significant incident that affects many people.
type MajorIncident struct {
	ID         string
	Reference  string
	Title      string
	Summary    string
	Status     string
	DeclaredBy *string
	ResolvedAt *time.Time
	ClosedAt   *time.Time
	Subscribed bool
	Tickets    int
	Version    int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Active reports whether the incident is still going on.
func (m MajorIncident) Active() bool { return m.Status != MIResolved && m.Status != MIClosed }

// MajorUpdate is one entry of the public status timeline.
type MajorUpdate struct {
	ID        string
	AuthorID  *string
	Status    string
	Body      string
	CreatedAt time.Time
}

// MajorStore is the persistence port of Major Incidents.
type MajorStore interface {
	InTx(ctx context.Context, fn func(tx pgx.Tx) error) error
	InsertMajorTx(ctx context.Context, tx pgx.Tx, m MajorIncident) (MajorIncident, error)
	LockMajorTx(ctx context.Context, tx pgx.Tx, id string) (MajorIncident, error)
	UpdateMajorTx(ctx context.Context, tx pgx.Tx, m MajorIncident) (MajorIncident, error)
	AddUpdateTx(ctx context.Context, tx pgx.Tx, id string, u MajorUpdate) (MajorUpdate, error)
	// GetMajor returns the incident with the viewer's subscription flag and the number of linked tickets.
	GetMajor(ctx context.Context, id, viewer string) (MajorIncident, error)
	ListMajor(ctx context.Context, viewer string, activeOnly bool, page Page) (MajorResult, error)
	Updates(ctx context.Context, id string) ([]MajorUpdate, error)
	SubscribeTx(ctx context.Context, tx pgx.Tx, id, userID string) error
	UnsubscribeTx(ctx context.Context, tx pgx.Tx, id, userID string) error
	// Subscribers returns up to limit subscribers after the given user id, in id order.
	Subscribers(ctx context.Context, tx pgx.Tx, id, after string, limit int) ([]string, error)
	// MajorTitle returns "reference · title" of an incident, or ErrNotFound.
	MajorTitle(ctx context.Context, tx pgx.Tx, id string) (string, error)
	// LinkTicketTx attaches a ticket (once); it reports the ticket's reporter and affected users.
	LinkTicketTx(ctx context.Context, tx pgx.Tx, majorID, ticketID string) (reporter, affected string, err error)
	// UnlinkTicketTx detaches a ticket from the incident; it reports whether the ticket was linked to it.
	UnlinkTicketTx(ctx context.Context, tx pgx.Tx, majorID, ticketID string) (bool, error)
	// MajorTickets lists the tickets linked to an incident (newest first, at most 200).
	MajorTickets(ctx context.Context, majorID string) ([]Ticket, error)
}

// MajorResult is one page of incidents.
type MajorResult struct {
	Items      []MajorIncident
	NextCursor string
}

// MajorService performs Major Incident operations. Audit actions:
// servicedesk.major_incident.declared, .investigating, .mitigating, .monitoring,
// .resolved, .closed, .update_posted and .ticket_linked. Messages are public and
// are not copied into audit.
type MajorService struct {
	store  MajorStore
	access TicketAccess
}

func NewMajorService(store MajorStore) *MajorService { return &MajorService{store: store} }

// WithTicketAccess sets the Ticket authorization. Without it no Ticket can be linked.
func (s *MajorService) WithTicketAccess(a TicketAccess) *MajorService {
	s.access = a
	return s
}

// Major Incident operations.
const (
	MOInvestigate = "investigate"
	MOMitigate    = "mitigate"
	MOMonitor     = "monitor"
	MOResolve     = "resolve"
	MOClose       = "close"
)

var majorRules = map[string]struct {
	from     []string
	to       string
	messageR bool
}{
	MOInvestigate: {[]string{MIIdentified}, MIInvestigating, false},
	MOMitigate:    {[]string{MIIdentified, MIInvestigating}, MIMitigating, false},
	MOMonitor:     {[]string{MIInvestigating, MIMitigating}, MIMonitoring, false},
	MOResolve:     {[]string{MIIdentified, MIInvestigating, MIMitigating, MIMonitoring}, MIResolved, true},
	MOClose:       {[]string{MIResolved}, MIClosed, false},
}

// MajorOperations lists the operations the status allows.
func MajorOperations(status string) []string {
	out := []string{}
	for _, op := range []string{MOInvestigate, MOMitigate, MOMonitor, MOResolve, MOClose} {
		if slices.Contains(majorRules[op].from, status) {
			out = append(out, op)
		}
	}
	return out
}

func majorState(m *MajorIncident) any {
	if m == nil {
		return nil
	}
	return map[string]any{"status": m.Status, "version": m.Version}
}

func majorRecord(ctx context.Context, tx pgx.Tx, c Caller, action string, before, after *MajorIncident, meta map[string]any) error {
	id := ""
	if after != nil {
		id = after.ID
	} else {
		id = before.ID
	}
	if len(meta) == 0 {
		meta = nil
	}
	return audit.Record(ctx, tx, audit.Change{Action: action, TargetType: "major_incident", TargetID: id, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Before: majorState(before), After: majorState(after), Metadata: meta})
}

func majorPublish(ctx context.Context, tx pgx.Tx, c Caller, typ string, m MajorIncident) error {
	var actor *string
	if c.Actor.UserID != "" {
		u := c.Actor.UserID
		actor = &u
	}
	return events.Publish(ctx, tx, events.Publication{Type: typ, ActorID: actor, CorrelationID: c.CorrelationID,
		Payload: map[string]any{"majorIncidentId": m.ID, "status": m.Status}})
}

// Declare opens a Major Incident with its first public message. Requires majorincidents.manage.
func (s *MajorService) Declare(ctx context.Context, c Caller, manage bool, title, summary string) (MajorIncident, error) {
	if err := c.validate(); err != nil {
		return MajorIncident{}, err
	}
	if !manage {
		return MajorIncident{}, ErrForbidden
	}
	title, err := cleanText(title, maxTitle, true, "title")
	if err != nil {
		return MajorIncident{}, err
	}
	summary, err = cleanText(summary, 2000, true, "message")
	if err != nil {
		return MajorIncident{}, err
	}
	m := MajorIncident{Title: title, Summary: summary, Status: MIIdentified}
	if c.Actor.UserID != "" {
		u := c.Actor.UserID
		m.DeclaredBy = &u
	}
	var out MajorIncident
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		out, err = s.store.InsertMajorTx(ctx, tx, m)
		if err != nil {
			return err
		}
		if _, err := s.store.AddUpdateTx(ctx, tx, out.ID, MajorUpdate{AuthorID: m.DeclaredBy, Status: out.Status, Body: summary}); err != nil {
			return err
		}
		if err := majorRecord(ctx, tx, c, "servicedesk.major_incident.declared", nil, &out, nil); err != nil {
			return err
		}
		return majorPublish(ctx, tx, c, "MajorIncidentDeclared", out)
	})
	return out, err
}

// Transition moves the incident forward and posts the message (required for resolve)
// as the new public summary. Requires majorincidents.manage.
func (s *MajorService) Transition(ctx context.Context, c Caller, manage bool, id string, expected *int, op, message string) (MajorIncident, error) {
	if err := c.validate(); err != nil {
		return MajorIncident{}, err
	}
	if !manage {
		return MajorIncident{}, ErrForbidden
	}
	r, ok := majorRules[op]
	if !ok {
		return MajorIncident{}, invalid("unknown operation %q", op)
	}
	message = strings.TrimSpace(message)
	if message != "" || r.messageR {
		m, err := cleanText(message, 2000, true, "message")
		if err != nil {
			return MajorIncident{}, err
		}
		message = m
	}
	var out MajorIncident
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockMajorTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if expected != nil && *expected != cur.Version {
			return ErrVersionConflict
		}
		if !slices.Contains(r.from, cur.Status) {
			return &InvalidTransitionError{Operation: op, From: cur.Status}
		}
		next := cur
		next.Status = r.to
		now := time.Now().UTC()
		switch r.to {
		case MIResolved:
			next.ResolvedAt = &now
		case MIClosed:
			next.ClosedAt = &now
		}
		if message != "" {
			next.Summary = message
		}
		out, err = s.store.UpdateMajorTx(ctx, tx, next)
		if err != nil {
			return err
		}
		body := message
		if body == "" {
			body = "-"
		}
		if _, err := s.store.AddUpdateTx(ctx, tx, id, MajorUpdate{AuthorID: userID(c), Status: out.Status, Body: body}); err != nil {
			return err
		}
		if err := majorRecord(ctx, tx, c, "servicedesk.major_incident."+r.to, &cur, &out, nil); err != nil {
			return err
		}
		return majorPublish(ctx, tx, c, "MajorIncidentUpdated", out)
	})
	return out, err
}

func userID(c Caller) *string {
	if c.Actor.UserID == "" {
		return nil
	}
	u := c.Actor.UserID
	return &u
}

// PostUpdate adds a public status message without changing the status. Requires
// majorincidents.manage; not for closed incidents.
func (s *MajorService) PostUpdate(ctx context.Context, c Caller, manage bool, id, body string) (MajorIncident, error) {
	if err := c.validate(); err != nil {
		return MajorIncident{}, err
	}
	if !manage {
		return MajorIncident{}, ErrForbidden
	}
	body, err := cleanText(body, 2000, true, "message")
	if err != nil {
		return MajorIncident{}, err
	}
	var out MajorIncident
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockMajorTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Status == MIClosed {
			return &InvalidTransitionError{Operation: "update", From: cur.Status}
		}
		next := cur
		next.Summary = body
		out, err = s.store.UpdateMajorTx(ctx, tx, next)
		if err != nil {
			return err
		}
		if _, err := s.store.AddUpdateTx(ctx, tx, id, MajorUpdate{AuthorID: userID(c), Status: out.Status, Body: body}); err != nil {
			return err
		}
		if err := majorRecord(ctx, tx, c, "servicedesk.major_incident.update_posted", &cur, &out, nil); err != nil {
			return err
		}
		return majorPublish(ctx, tx, c, "MajorIncidentUpdated", out)
	})
	return out, err
}

// LinkTicket attaches a ticket to an incident; its reporter and affected User are
// subscribed so they hear about progress instead of chasing. Requires majorincidents.manage.
func (s *MajorService) LinkTicket(ctx context.Context, c Caller, manage bool, id, ticketID string) error {
	if err := c.validate(); err != nil {
		return err
	}
	if !manage {
		return ErrForbidden
	}
	// Linking needs view access to the Ticket's Queue, with one answer for a Ticket that does not exist and one the
	// caller may not see; it also subscribes the Ticket's people, so it must not reach into Queues the caller cannot see.
	if s.access == nil {
		return ErrNotFound
	}
	ok, err := s.access.CanViewTicket(ctx, c.Actor.UserID, ticketID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockMajorTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if !cur.Active() {
			return &InvalidTransitionError{Operation: "link_ticket", From: cur.Status}
		}
		reporter, affected, err := s.store.LinkTicketTx(ctx, tx, id, ticketID)
		if errors.Is(err, ErrAlreadyLinked) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Change{Action: "servicedesk.ticket.major_incident_linked", TargetType: "ticket", TargetID: strings.ToLower(ticketID),
			Actor: c.Actor, CorrelationID: c.CorrelationID, Metadata: map[string]any{"majorIncidentId": cur.ID}}); err != nil {
			return err
		}
		for _, u := range []string{reporter, affected} {
			if err := s.store.SubscribeTx(ctx, tx, id, u); err != nil {
				return err
			}
		}
		return audit.Record(ctx, tx, audit.Change{Action: "servicedesk.major_incident.ticket_linked", TargetType: "major_incident", TargetID: cur.ID,
			Actor: c.Actor, CorrelationID: c.CorrelationID, Metadata: map[string]any{"ticketId": ticketID}})
	})
}

// UnlinkTicket detaches a ticket from an incident that is not closed. Requires majorincidents.manage and view access
// to the Ticket's Queue (the same answer for a Ticket that does not exist and one the caller may not see). The
// people subscribed through the link stay subscribed: following an incident is their own choice from then on.
func (s *MajorService) UnlinkTicket(ctx context.Context, c Caller, manage bool, id, ticketID string) error {
	if err := c.validate(); err != nil {
		return err
	}
	if !manage {
		return ErrForbidden
	}
	if s.access == nil {
		return ErrNotFound
	}
	ok, err := s.access.CanViewTicket(ctx, c.Actor.UserID, ticketID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockMajorTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Status == MIClosed {
			return &InvalidTransitionError{Operation: "unlink_ticket", From: cur.Status}
		}
		was, err := s.store.UnlinkTicketTx(ctx, tx, id, ticketID)
		if err != nil || !was {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Change{Action: "servicedesk.ticket.major_incident_unlinked", TargetType: "ticket", TargetID: strings.ToLower(ticketID),
			Actor: c.Actor, CorrelationID: c.CorrelationID, Metadata: map[string]any{"majorIncidentId": cur.ID}}); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Change{Action: "servicedesk.major_incident.ticket_unlinked", TargetType: "major_incident", TargetID: cur.ID,
			Actor: c.Actor, CorrelationID: c.CorrelationID, Metadata: map[string]any{"ticketId": strings.ToLower(ticketID)}})
	})
}

// Subscribe lets any signed-in User follow an active incident instead of reporting it again.
func (s *MajorService) Subscribe(ctx context.Context, c Caller, user string, id string, on bool) error {
	if err := c.validate(); err != nil {
		return err
	}
	if user == "" {
		return ErrForbidden
	}
	// Following is a personal preference: no row lock, so a rush of subscribers during an
	// outage does not queue up behind the staff's status updates.
	cur, err := s.store.GetMajor(ctx, id, user)
	if err != nil {
		return err
	}
	if on && !cur.Active() {
		return &InvalidTransitionError{Operation: "subscribe", From: cur.Status}
	}
	return s.store.InTx(ctx, func(tx pgx.Tx) error {
		if !on {
			return s.store.UnsubscribeTx(ctx, tx, id, user)
		}
		return s.store.SubscribeTx(ctx, tx, id, user)
	})
}

// MajorDetail is an incident with its public timeline.
type MajorDetail struct {
	Incident MajorIncident
	Updates  []MajorUpdate
	// Tickets are the linked Tickets the reader may view (their Queue grants, or being reporter or affected); the
	// others are not listed, so Incident.Tickets (the total) can be larger than len(Tickets).
	Tickets    []Ticket
	Operations []string
}

// Get returns an incident (every signed-in User may read incidents).
func (s *MajorService) Get(ctx context.Context, user string, manage bool, id string) (MajorDetail, error) {
	if user == "" {
		return MajorDetail{}, ErrForbidden
	}
	m, err := s.store.GetMajor(ctx, id, user)
	if err != nil {
		return MajorDetail{}, err
	}
	updates, err := s.store.Updates(ctx, id)
	if err != nil {
		return MajorDetail{}, err
	}
	d := MajorDetail{Incident: m, Updates: updates, Tickets: []Ticket{}, Operations: []string{}}
	if s.access != nil && m.Tickets > 0 {
		linked, err := s.store.MajorTickets(ctx, id)
		if err != nil {
			return MajorDetail{}, err
		}
		if d.Tickets, err = s.access.VisibleTickets(ctx, user, linked); err != nil {
			return MajorDetail{}, err
		}
	}
	if manage {
		d.Operations = MajorOperations(m.Status)
	}
	return d, nil
}

// List returns incidents, newest first (every signed-in User).
func (s *MajorService) List(ctx context.Context, user string, activeOnly bool, page Page) (MajorResult, error) {
	if user == "" {
		return MajorResult{}, ErrForbidden
	}
	return s.store.ListMajor(ctx, user, activeOnly, page.Normalize())
}
