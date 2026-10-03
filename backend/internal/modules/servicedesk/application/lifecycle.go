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
