package application

// Operation is an explicit Task state-changing operation. There is
// deliberately no generic "set status" (docs/domain/state-machines.md).
type Operation string

const (
	OpStart    Operation = "start"
	OpBlock    Operation = "block"
	OpUnblock  Operation = "unblock"
	OpComplete Operation = "complete"
	OpCancel   Operation = "cancel"
	OpReopen   Operation = "reopen"
)

type transition struct {
	from []string
	to   string
	// reason is required (blocked, cancelled and reopened tasks carry or
	// explain one).
	reason bool
	// workable operations may also be performed by a caller with tasks.work on
	// a task assigned to it; the others need tasks.manage.
	workable bool
}

var transitions = map[Operation]transition{
	OpStart:    {from: []string{StatusOpen, StatusBlocked}, to: StatusInProgress, workable: true},
	OpBlock:    {from: []string{StatusOpen, StatusInProgress}, to: StatusBlocked, reason: true, workable: true},
	OpUnblock:  {from: []string{StatusBlocked}, to: StatusOpen, workable: true},
	OpComplete: {from: []string{StatusOpen, StatusInProgress}, to: StatusCompleted, workable: true},
	OpCancel:   {from: []string{StatusOpen, StatusInProgress, StatusBlocked}, to: StatusCancelled, reason: true},
	OpReopen:   {from: []string{StatusCompleted, StatusCancelled}, to: StatusOpen, reason: true},
}

// next returns the status op leads to from the task's current status.
func next(op Operation, from string) (string, error) {
	tr, ok := transitions[op]
	if !ok || !contains(tr.from, from) {
		return "", &InvalidTransitionError{Operation: string(op), From: from}
	}
	return tr.to, nil
}
