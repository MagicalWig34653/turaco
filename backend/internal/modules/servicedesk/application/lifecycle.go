package application

import "slices"

// Operations. The lifecycle is explicit: there is no generic status update.
const (
	OpStart   = "start"
	OpWait    = "wait"
	OpResume  = "resume"
	OpResolve = "resolve"
	OpClose   = "close"
	OpReopen  = "reopen"
	OpCancel  = "cancel"
)

type rule struct {
	from []string
	to   string
	// reason says whether the operation needs a reason (stored while the status lasts).
	reasonRequired bool
	// ownerMay lets the reporter or affected User perform it without tickets.manage.
	ownerMay bool
}

var rules = map[string]rule{
	OpStart:   {from: []string{StatusNew, StatusOpen}, to: StatusInProgress},
	OpWait:    {from: []string{StatusOpen, StatusInProgress}, to: StatusWaiting},
	OpResume:  {from: []string{StatusWaiting}, to: StatusInProgress},
	OpResolve: {from: []string{StatusNew, StatusOpen, StatusInProgress, StatusWaiting}, to: StatusResolved},
	OpClose:   {from: []string{StatusResolved}, to: StatusClosed, ownerMay: true},
	OpReopen:  {from: []string{StatusResolved, StatusClosed}, to: StatusOpen, reasonRequired: true, ownerMay: true},
	OpCancel:  {from: []string{StatusNew, StatusOpen, StatusInProgress, StatusWaiting}, to: StatusCancelled, reasonRequired: true, ownerMay: true},
}

// AllowedOperations lists what the caller may do in the ticket's status.
func AllowedOperations(t Ticket, p Principal) []string {
	owner := t.ReporterID == p.UserID || t.AffectedUserID == p.UserID
	out := []string{}
	for _, name := range []string{OpStart, OpWait, OpResume, OpResolve, OpClose, OpReopen, OpCancel} {
		r := rules[name]
		if !slices.Contains(r.from, t.Status) {
			continue
		}
		if p.Manage || (owner && r.ownerMay) {
			// An owner can only cancel before work started.
			if name == OpCancel && !p.Manage && t.Status != StatusNew && t.Status != StatusOpen {
				continue
			}
			out = append(out, name)
		}
	}
	return out
}

// Abilities is what the caller may do with one Ticket, computed with the same rules the operations enforce
// (queue grants, global permissions, reporter or affected User, and the status). The UI shows actions from it; the
// backend still authorizes every operation.
type Abilities struct {
	// Comment adds a public comment.
	Comment bool
	// InternalComment adds an internal comment (staff of the Queue).
	InternalComment bool
	// Assign changes assignee or routing Team.
	Assign bool
	// SetPriority changes the priority.
	SetPriority bool
	// Transition is true when at least one lifecycle operation in AllowedOperations is possible.
	Transition bool
	// MoveQueue moves the Ticket to another Queue.
	MoveQueue bool
	// MarkDuplicate cancels the Ticket as a duplicate of another one.
	MarkDuplicate bool
}

// AbilitiesOf computes the Abilities of the caller (with their authority ep over the Ticket's Queue) for a Ticket.
// canMove says whether the deployment has Queues, which a move needs.
func AbilitiesOf(t Ticket, ep Principal, canMove bool) Abilities {
	owner := ep.UserID != "" && (t.ReporterID == ep.UserID || t.AffectedUserID == ep.UserID)
	open := t.Status != StatusClosed && t.Status != StatusCancelled
	return Abilities{
		Comment:         (ep.Manage || owner) && open,
		InternalComment: ep.Manage && open,
		Assign:          ep.Manage && !slices.Contains([]string{StatusResolved, StatusClosed, StatusCancelled}, t.Status),
		SetPriority:     ep.Manage,
		Transition:      len(AllowedOperations(t, ep)) > 0,
		MoveQueue:       ep.Manage && canMove,
		MarkDuplicate:   ep.Manage && slices.Contains(rules[OpCancel].from, t.Status),
	}
}
