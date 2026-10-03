package application

import (
	"slices"
	"testing"
)

func TestLifecycleMatrix(t *testing.T) {
	// operation -> statuses it is allowed from, the state machine in docs/domain/state-machines.md.
	want := map[string][]string{
		OpMakeAvailable:      {StatusReceived, StatusReturned},
		OpReserve:            {StatusAvailable},
		OpReleaseReservation: {StatusReserved},
		OpAssign:             {StatusAvailable},
		OpAssignReserved:     {StatusReserved},
		OpReassign:           {StatusAssigned},
		OpReturn:             {StatusAssigned},
		OpSendToRepair:       {StatusAvailable, StatusReturned, StatusAssigned},
		OpFinishRepair:       {StatusInRepair},
		OpRetire:             {StatusAvailable, StatusReturned},
		OpDispose:            {StatusRetired},
		OpMarkLost:           {StatusReceived, StatusAvailable, StatusReserved, StatusAssigned, StatusReturned, StatusInRepair},
		OpRecover:            {StatusLost},
	}
	if len(want) != len(rules) {
		t.Fatalf("%d operations documented, %d implemented", len(want), len(rules))
	}
	for op, from := range want {
		got := slices.Clone(rules[op].from)
		slices.Sort(got)
		slices.Sort(from)
		if !slices.Equal(got, from) {
			t.Errorf("%s allowed from %v, want %v", op, got, from)
		}
	}
}

func TestDisposedIsTerminalAndInternalOperationsAreHidden(t *testing.T) {
	for op, r := range rules {
		if slices.Contains(r.from, StatusDisposed) {
			t.Errorf("%s may start from disposed", op)
		}
	}
	if ops := AllowedOperations(StatusDisposed); len(ops) != 0 {
		t.Errorf("disposed allows %v", ops)
	}
	for _, status := range Statuses {
		for _, op := range AllowedOperations(status) {
			if rules[op].internal {
				t.Errorf("internal operation %s is offered to the API", op)
			}
		}
	}
	if got := AllowedOperations(StatusAvailable); !slices.Contains(got, OpAssign) || slices.Contains(got, OpReturn) {
		t.Errorf("available allows %v", got)
	}
}

func TestEveryOperationEndsInAKnownStatus(t *testing.T) {
	for op, r := range rules {
		if !slices.Contains(Statuses, r.to) {
			t.Errorf("%s ends in unknown status %q", op, r.to)
		}
		if r.event == "" {
			t.Errorf("%s publishes no event", op)
		}
	}
}
