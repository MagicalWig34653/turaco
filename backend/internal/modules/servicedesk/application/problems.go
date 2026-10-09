package application

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// Problem statuses (docs/domain/state-machines.md). A Known Error is a Problem in
// status known_error or resolution_planned: cause and workaround are understood.
const (
	PRNew           = "new"
	PRInvestigating = "under_investigation"
	PRCauseKnown    = "cause_identified"
	PRKnownError    = "known_error"
	PRPlanned       = "resolution_planned"
	PRResolved      = "resolved"
	PRClosed        = "closed"
)

// ProblemStatuses lists every problem status.
var ProblemStatuses = []string{PRNew, PRInvestigating, PRCauseKnown, PRKnownError, PRPlanned, PRResolved, PRClosed}

// Problem is the underlying, possibly repeating cause of incidents.
type Problem struct {
	ID          string
	Reference   string
	Title       string
	Description *string
	Status      string
	Cause       *string
	Workaround  *string
	Resolution  *string
	OwnerID     *string
	CreatedBy   *string
	ResolvedAt  *time.Time
	ClosedAt    *time.Time
	Tickets     int
	Version     int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ProblemStore is the persistence port of Problems.
type ProblemStore interface {
	InTx(ctx context.Context, fn func(tx pgx.Tx) error) error
	InsertProblemTx(ctx context.Context, tx pgx.Tx, p Problem) (Problem, error)
	LockProblemTx(ctx context.Context, tx pgx.Tx, id string) (Problem, error)
	UpdateProblemTx(ctx context.Context, tx pgx.Tx, p Problem) (Problem, error)
	GetProblem(ctx context.Context, id string) (Problem, error)
	ListProblems(ctx context.Context, status string, page Page) (ProblemResult, error)
	LinkProblemTicketTx(ctx context.Context, tx pgx.Tx, problemID, ticketID, by string) (bool, error)
	UnlinkProblemTicketTx(ctx context.Context, tx pgx.Tx, problemID, ticketID string) error
	// ProblemTickets lists the tickets of a problem (newest first).
	ProblemTickets(ctx context.Context, problemID string) ([]Ticket, error)
	// KnownErrorsOfTicket lists the known errors (with workaround) a ticket is linked to.
	KnownErrorsOfTicket(ctx context.Context, ticketID string) ([]Problem, error)
}

// ProblemResult is one page of problems.
type ProblemResult struct {
	Items      []Problem
	NextCursor string
}

// ProblemService performs Problem operations. Audit actions: servicedesk.problem.created,
// .updated and one per lifecycle operation, .ticket_linked and .ticket_unlinked. Text is not
// copied into audit.
type ProblemService struct {
	store  ProblemStore
	dir    Directory
	access TicketAccess
}

// WithTicketAccess sets the Ticket authorization. Without it no Ticket can be linked and none is listed.
func (s *ProblemService) WithTicketAccess(a TicketAccess) *ProblemService {
	s.access = a
	return s
}

func NewProblemService(store ProblemStore, dir Directory) *ProblemService {
	return &ProblemService{store: store, dir: dir}
}

// Problem operations.
const (
	POInvestigate    = "investigate"
	POIdentifyCause  = "identify_cause"
	POMarkKnownError = "mark_known_error"
	POPlanResolution = "plan_resolution"
	POResolve        = "resolve"
	POClose          = "close"
)

type problemRule struct {
	from []string
	to   string
	// text names the field the operation needs: cause, workaround, resolution or "".
	text string
}

var problemRules = map[string]problemRule{
	POInvestigate:    {[]string{PRNew}, PRInvestigating, ""},
	POIdentifyCause:  {[]string{PRNew, PRInvestigating}, PRCauseKnown, "cause"},
	POMarkKnownError: {[]string{PRCauseKnown}, PRKnownError, "workaround"},
	POPlanResolution: {[]string{PRKnownError}, PRPlanned, ""},
	POResolve:        {[]string{PRNew, PRInvestigating, PRCauseKnown, PRKnownError, PRPlanned}, PRResolved, "resolution"},
	POClose:          {[]string{PRResolved}, PRClosed, ""},
}

// ProblemOperations lists the operations the status allows.
func ProblemOperations(status string) []string {
	out := []string{}
	for _, op := range []string{POInvestigate, POIdentifyCause, POMarkKnownError, POPlanResolution, POResolve, POClose} {
		if slices.Contains(problemRules[op].from, status) {
			out = append(out, op)
		}
	}
	return out
}

func problemState(p *Problem) any {
	if p == nil {
		return nil
	}
	return map[string]any{"status": p.Status, "owner": p.OwnerID, "version": p.Version}
}

func problemRecord(ctx context.Context, tx pgx.Tx, c Caller, action string, before, after *Problem, meta map[string]any) error {
	id := ""
	if after != nil {
		id = after.ID
	} else {
		id = before.ID
	}
	if len(meta) == 0 {
		meta = nil
	}
	return audit.Record(ctx, tx, audit.Change{Action: action, TargetType: "problem", TargetID: id, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Before: problemState(before), After: problemState(after), Metadata: meta})
}

// ProblemPrincipal is the caller's problem authority: Staff (tickets.view or
// tickets.manage) reads, Manage (problems.manage) writes. Employees see nothing.
type ProblemPrincipal struct {
	UserID string
	Staff  bool
	Manage bool
}

// problems.manage also reads problems and the summaries of the tickets linked to them.
func (p ProblemPrincipal) canRead() bool { return p.Staff || p.Manage }

// CreateProblem opens a problem. Requires problems.manage.
func (s *ProblemService) CreateProblem(ctx context.Context, c Caller, p ProblemPrincipal, title, description string) (Problem, error) {
	if err := c.validate(); err != nil {
		return Problem{}, err
	}
	if !p.Manage {
		return Problem{}, ErrForbidden
	}
	title, err := cleanText(title, maxTitle, true, "title")
	if err != nil {
		return Problem{}, err
	}
	description, err = cleanText(description, maxText, false, "description")
	if err != nil {
		return Problem{}, err
	}
	pr := Problem{Title: title, Description: strPtr(description), Status: PRNew}
	if c.Actor.UserID != "" {
		u := c.Actor.UserID
		pr.CreatedBy, pr.OwnerID = &u, &u
	}
	var out Problem
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		out, err = s.store.InsertProblemTx(ctx, tx, pr)
		if err != nil {
			return err
		}
		return problemRecord(ctx, tx, c, "servicedesk.problem.created", nil, &out, nil)
	})
	return out, err
}

// ProblemParams are the inputs of a lifecycle operation: Text is the cause,
// workaround or resolution the operation needs.
type ProblemParams struct{ Text string }

// Transition performs a lifecycle operation. Requires problems.manage.
func (s *ProblemService) Transition(ctx context.Context, c Caller, p ProblemPrincipal, id string, expected *int, op string, params ProblemParams) (Problem, error) {
	if err := c.validate(); err != nil {
		return Problem{}, err
	}
	if !p.Manage {
		return Problem{}, ErrForbidden
	}
	r, ok := problemRules[op]
	if !ok {
		return Problem{}, invalid("unknown operation %q", op)
	}
	text := ""
	if r.text != "" {
		t, err := cleanText(params.Text, maxText, true, r.text)
		if err != nil {
			return Problem{}, err
		}
		text = t
	}
	var out Problem
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockProblemTx(ctx, tx, id)
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
		switch r.text {
		case "cause":
			next.Cause = &text
		case "workaround":
			next.Workaround = &text
		case "resolution":
			next.Resolution = &text
		}
		switch r.to {
		case PRResolved:
			next.ResolvedAt = &now
		case PRClosed:
			next.ClosedAt = &now
		}
		out, err = s.store.UpdateProblemTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return problemRecord(ctx, tx, c, "servicedesk.problem."+op, &cur, &out, nil)
	})
	return out, err
}

// SetOwner assigns the owner of a problem (an active User). Requires problems.manage.
func (s *ProblemService) SetOwner(ctx context.Context, c Caller, p ProblemPrincipal, id string, expected *int, owner string) (Problem, error) {
	if err := c.validate(); err != nil {
		return Problem{}, err
	}
	if !p.Manage {
		return Problem{}, ErrForbidden
	}
	active, err := s.dir.ActiveUsers(ctx, []string{owner})
	if err != nil {
		return Problem{}, err
	}
	if !active[owner] {
		return Problem{}, ErrUserInvalid
	}
	var out Problem
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockProblemTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if expected != nil && *expected != cur.Version {
			return ErrVersionConflict
		}
		if cur.Status == PRClosed {
			return &InvalidTransitionError{Operation: "set_owner", From: cur.Status}
		}
		next := cur
		next.OwnerID = &owner
		out, err = s.store.UpdateProblemTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return problemRecord(ctx, tx, c, "servicedesk.problem.updated", &cur, &out, map[string]any{"changedFields": []string{"owner"}})
	})
	return out, err
}

// LinkTicket links (link=true) or unlinks a ticket to a problem that is not closed.
// Requires problems.manage.
func (s *ProblemService) LinkTicket(ctx context.Context, c Caller, p ProblemPrincipal, id, ticketID string, link bool) error {
	if err := c.validate(); err != nil {
		return err
	}
	if !p.Manage {
		return ErrForbidden
	}
	if link {
		// Linking needs view access to the Ticket's Queue. The answer is the same for a Ticket that does not exist
		// and one the caller may not see, so linking cannot be used to probe for Tickets.
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
	}
	return s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockProblemTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Status == PRClosed {
			return &InvalidTransitionError{Operation: "link_ticket", From: cur.Status}
		}
		action := "servicedesk.problem.ticket_unlinked"
		if link {
			action = "servicedesk.problem.ticket_linked"
			if _, err := s.store.LinkProblemTicketTx(ctx, tx, cur.ID, ticketID, c.Actor.UserID); err != nil {
				return err
			}
		} else if err := s.store.UnlinkProblemTicketTx(ctx, tx, cur.ID, ticketID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Change{Action: action, TargetType: "problem", TargetID: cur.ID, Actor: c.Actor,
			CorrelationID: c.CorrelationID, Metadata: map[string]any{"ticketId": strings.ToLower(ticketID)}})
	})
}

// ProblemDetail is a problem with its tickets and the operations its status allows.
type ProblemDetail struct {
	Problem    Problem
	Tickets    []Ticket
	Operations []string
}

// Get returns a problem (staff only).
func (s *ProblemService) Get(ctx context.Context, p ProblemPrincipal, id string) (ProblemDetail, error) {
	if !p.canRead() {
		return ProblemDetail{}, ErrNotFound
	}
	pr, err := s.store.GetProblem(ctx, id)
	if err != nil {
		return ProblemDetail{}, err
	}
	tickets, err := s.store.ProblemTickets(ctx, pr.ID)
	if err != nil {
		return ProblemDetail{}, err
	}
	// Linked Tickets of Queues the reader may not view are not listed (and the rest is shaped for them).
	if s.access == nil {
		tickets = nil
	} else if tickets, err = s.access.VisibleTickets(ctx, p.UserID, tickets); err != nil {
		return ProblemDetail{}, err
	}
	d := ProblemDetail{Problem: pr, Tickets: tickets, Operations: []string{}}
	if p.Manage {
		d.Operations = ProblemOperations(pr.Status)
	}
	return d, nil
}

// List returns problems, newest first (staff only).
func (s *ProblemService) List(ctx context.Context, p ProblemPrincipal, status string, page Page) (ProblemResult, error) {
	if !p.canRead() {
		return ProblemResult{}, ErrForbidden
	}
	if status != "" && !slices.Contains(ProblemStatuses, status) {
		return ProblemResult{}, invalid("unknown status")
	}
	return s.store.ListProblems(ctx, status, page.Normalize())
}

// KnownErrorsOfTicket returns the known errors a ticket is linked to, so staff see the
// workaround while working the ticket (staff only; others get nothing).
func (s *ProblemService) KnownErrorsOfTicket(ctx context.Context, p ProblemPrincipal, ticketID string) ([]Problem, error) {
	if !p.canRead() {
		return nil, ErrForbidden
	}
	return s.store.KnownErrorsOfTicket(ctx, ticketID)
}
