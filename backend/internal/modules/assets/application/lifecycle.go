package application

import "slices"

// Operation names. The lifecycle is explicit: there is no generic status update.
const (
	OpMakeAvailable      = "make_available"
	OpReserve            = "reserve"
	OpReleaseReservation = "release_reservation"
	OpAssign             = "assign"
	OpAssignReserved     = "assign_reserved"
	OpReassign           = "reassign"
	OpReturn             = "return"
	OpSendToRepair       = "send_to_repair"
	OpFinishRepair       = "finish_repair"
	OpRetire             = "retire"
	OpDispose            = "dispose"
	OpMarkLost           = "mark_lost"
	OpRecover            = "recover"
)

type rule struct {
	from []string
	to   string
	// internal operations are performed by Inventory through the public contract only.
	internal bool
	// assignee operations need an assignee and open an assignment.
	assignee bool
	// closesAssignment ends the active assignment.
	closesAssignment bool
	// reasonRequired operations need a reason, stored while the status lasts.
	reasonRequired bool
	// keepsReason operations store the optional reason; others clear it.
	event string
}

var rules = map[string]rule{
	OpMakeAvailable:      {from: []string{StatusReceived, StatusReturned}, to: StatusAvailable, event: "AssetStatusChanged"},
	OpReserve:            {from: []string{StatusAvailable}, to: StatusReserved, internal: true, event: "AssetStatusChanged"},
	OpReleaseReservation: {from: []string{StatusReserved}, to: StatusAvailable, internal: true, event: "AssetStatusChanged"},
	OpAssign:             {from: []string{StatusAvailable}, to: StatusAssigned, assignee: true, event: "AssetAssigned"},
	OpAssignReserved:     {from: []string{StatusReserved}, to: StatusAssigned, assignee: true, internal: true, event: "AssetAssigned"},
	OpReassign:           {from: []string{StatusAssigned}, to: StatusAssigned, assignee: true, closesAssignment: true, event: "AssetAssigned"},
	OpReturn:             {from: []string{StatusAssigned}, to: StatusReturned, closesAssignment: true, event: "AssetReturned"},
	OpSendToRepair:       {from: []string{StatusAvailable, StatusReturned, StatusAssigned}, to: StatusInRepair, closesAssignment: true, reasonRequired: true, event: "AssetStatusChanged"},
	OpFinishRepair:       {from: []string{StatusInRepair}, to: StatusAvailable, event: "AssetStatusChanged"},
	OpRetire:             {from: []string{StatusAvailable, StatusReturned}, to: StatusRetired, reasonRequired: true, event: "AssetStatusChanged"},
	OpDispose:            {from: []string{StatusRetired}, to: StatusDisposed, reasonRequired: true, event: "AssetStatusChanged"},
	OpMarkLost:           {from: []string{StatusReceived, StatusAvailable, StatusAssigned, StatusReturned, StatusInRepair}, to: StatusLost, closesAssignment: true, reasonRequired: true, event: "AssetStatusChanged"},
	OpRecover:            {from: []string{StatusLost}, to: StatusAvailable, reasonRequired: true, event: "AssetStatusChanged"},
}

// keepsReason reports whether the status stores the reason given for entering it.
func keepsReason(status string) bool {
	return slices.Contains([]string{StatusInRepair, StatusRetired, StatusDisposed, StatusLost}, status)
}

// AllowedOperations lists the operations (HTTP-visible ones only) the status allows.
func AllowedOperations(status string) []string {
	var out []string
	for _, name := range []string{OpMakeAvailable, OpAssign, OpReassign, OpReturn, OpSendToRepair, OpFinishRepair, OpRetire, OpDispose, OpMarkLost, OpRecover} {
		if r := rules[name]; slices.Contains(r.from, status) {
			out = append(out, name)
		}
	}
	return out
}
