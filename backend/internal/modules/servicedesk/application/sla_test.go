package application

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type slaProbe struct {
	Store
	calls  []string
	reads  int
	ticket Ticket
}

func (p *slaProbe) ApplySLATx(_ context.Context, _ pgx.Tx, _ Ticket, operation, reason string, _ Caller, _ time.Time) ([]map[string]any, error) {
	p.calls = append(p.calls, operation+":"+reason)
	return nil, nil
}
func (p *slaProbe) SLAClocks(context.Context, string) ([]SLAClock, error) {
	p.reads++
	return []SLAClock{{Kind: "resolution", Status: "running"}}, nil
}
func (p *slaProbe) Get(context.Context, string) (Ticket, error)               { return p.ticket, nil }
func (p *slaProbe) Comments(context.Context, string, bool) ([]Comment, error) { return nil, nil }

type slaDir struct{}

func (slaDir) ActiveUsers(context.Context, []string) (map[string]bool, error) { return nil, nil }
func (slaDir) ActiveTeams(context.Context, []string) (map[string]bool, error) { return nil, nil }
func (slaDir) UserNames(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (slaDir) TeamNames(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

func TestSwitchOffSkipsNewClocksButMaintainsExisting(t *testing.T) {
	probe := &slaProbe{}
	s := NewService(probe, nil, nil).WithSLA(func(context.Context) (bool, error) { return false, nil })
	meta := map[string]any{}
	on, err := s.slaOn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.startSLA(context.Background(), nil, Ticket{}, Caller{}, on, meta); err != nil {
		t.Fatal(err)
	}
	if len(probe.calls) != 0 {
		t.Fatalf("created while off: %v", probe.calls)
	}
	for _, op := range []string{"wait", "resume", "resolve", "reopen", "set_priority", "move_queue", "cancel"} {
		if err := s.applySLA(context.Background(), nil, Ticket{}, op, "", Caller{}, time.Now(), meta); err != nil {
			t.Fatal(err)
		}
	}
	if len(probe.calls) != 7 {
		t.Fatalf("existing clocks not maintained: %v", probe.calls)
	}
}

func TestRequesterNeverReceivesSLAEvenWithStaffPermission(t *testing.T) {
	probe := &slaProbe{ticket: Ticket{ID: "ticket", ReporterID: "owner", AffectedUserID: "other", QueueID: "queue", Status: StatusNew}}
	s := NewService(probe, slaDir{}, nil).WithSLA(func(context.Context) (bool, error) { return true, nil })
	d, err := s.Get(context.Background(), Principal{UserID: "owner", View: true, Manage: true}, "ticket")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.SLA) != 0 || probe.reads != 0 {
		t.Fatalf("requester read SLA: %+v, reads=%d", d.SLA, probe.reads)
	}
	d, err = s.Get(context.Background(), Principal{UserID: "staff", View: true}, "ticket")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.SLA) != 1 || probe.reads != 1 {
		t.Fatalf("staff SLA = %+v, reads=%d", d.SLA, probe.reads)
	}
}
