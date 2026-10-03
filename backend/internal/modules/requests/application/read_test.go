package application

import "testing"

func TestActionsFollowStatusAndAuthority(t *testing.T) {
	own := func(status string) Request { return Request{RequesterID: "u1", Status: status} }
	cases := []struct {
		name string
		p    Principal
		r    Request
		want string
	}{
		{"requester while pending", Principal{UserID: "u1"}, own(StatusPendingApproval), "cancel"},
		{"requester in fulfillment", Principal{UserID: "u1"}, own(StatusInFulfillment), ""},
		{"stranger", Principal{UserID: "u2"}, own(StatusPendingApproval), ""},
		{"viewer", Principal{UserID: "u2", View: true}, own(StatusInFulfillment), ""},
		{"manager pending", Principal{UserID: "u2", Manage: true}, own(StatusPendingApproval), "cancel"},
		{"manager in fulfillment", Principal{UserID: "u2", Manage: true}, own(StatusInFulfillment), "cancel,put_on_hold,complete"},
		{"manager waiting", Principal{UserID: "u2", Manage: true}, own(StatusWaiting), "cancel,resume,complete"},
		{"manager completed", Principal{UserID: "u2", Manage: true}, own(StatusCompleted), ""},
		{"manager rejected", Principal{UserID: "u2", Manage: true}, own(StatusRejected), ""},
		{"manager cancelled", Principal{UserID: "u2", Manage: true}, own(StatusCancelled), ""},
	}
	for _, c := range cases {
		got := ""
		for i, a := range actions(c.p, c.r) {
			if i > 0 {
				got += ","
			}
			got += a
		}
		if got != c.want {
			t.Errorf("%s: actions = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestReasonValidation(t *testing.T) {
	for name, in := range map[string]string{"blank": "  ", "long": string(make([]byte, 501)), "override": "x‮", "control": "a\x00b"} {
		if _, err := cleanReason(in); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if got, err := cleanReason("  needed  "); err != nil || got != "needed" {
		t.Errorf("got %q %v", got, err)
	}
}

func TestTerminalAndLabel(t *testing.T) {
	for status, want := range map[string]bool{StatusCompleted: true, StatusRejected: true, StatusCancelled: true, StatusInFulfillment: false, StatusPendingApproval: false, StatusWaiting: false} {
		if (Request{Status: status}).Terminal() != want {
			t.Errorf("Terminal(%s) != %v", status, want)
		}
	}
	if l := (Request{Reference: "REQ-2026-000001", CatalogItemTitle: "Laptop"}).Label(); l != "REQ-2026-000001 · Laptop" {
		t.Errorf("label = %q", l)
	}
}
