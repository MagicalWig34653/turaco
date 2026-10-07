package repository_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/remoteaccess"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/remoteaccess/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/remoteaccess/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

const provider = "rustdesk"

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// newPeer returns a random nine-digit peer id.
func newPeer() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	n := uint64(b[0])<<32 | uint64(b[1])<<24 | uint64(b[2])<<16 | uint64(b[3])<<8 | uint64(b[4])
	return fmt.Sprintf("%09d", 100000000+n%900000000)
}

type fakeDevices struct {
	mu sync.Mutex
	m  map[string]application.DeviceInfo
}

func (f *fakeDevices) Device(_ context.Context, id string) (application.DeviceInfo, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.m[id]
	return d, ok, nil
}

type fakeTickets struct {
	m map[string]application.TicketInfo
}

func (f *fakeTickets) Ticket(_ context.Context, id string) (application.TicketInfo, bool, error) {
	t, ok := f.m[id]
	return t, ok, nil
}

type fakeHolders struct{ m map[string]string }

func (f *fakeHolders) UserHolders(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		if u, ok := f.m[id]; ok {
			out[id] = u
		}
	}
	return out, nil
}

type fakeApprovals struct {
	mu        sync.Mutex
	byID      map[string]*application.ApprovalInfo
	bySubject map[string][]string
	cancelled []string
}

func (f *fakeApprovals) RequestInTx(_ context.Context, _ pgx.Tx, _ audit.Actor, _, subjectID, _ string, a application.Approver, _ []string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := newID()
	f.byID[id] = &application.ApprovalInfo{ID: id, Status: "pending", ApproverUserID: a.UserID, ApproverTeamID: a.TeamID}
	f.bySubject[subjectID] = append(f.bySubject[subjectID], id)
	return id, nil
}

func (f *fakeApprovals) CancelBySubjectInTx(_ context.Context, _ pgx.Tx, _ audit.Actor, _, subjectID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, subjectID)
	for _, id := range f.bySubject[subjectID] {
		if f.byID[id].Status == "pending" {
			f.byID[id].Status = "cancelled"
		}
	}
	return nil
}

func (f *fakeApprovals) ForSubject(_ context.Context, subjectID string) ([]application.ApprovalInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []application.ApprovalInfo
	for _, id := range f.bySubject[subjectID] {
		out = append(out, *f.byID[id])
	}
	return out, nil
}

func (f *fakeApprovals) decide(id, status, by string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byID[id].Status, f.byID[id].DecidedByUserID = status, &by
}

type fakeApprovers struct {
	perms map[string]map[string]struct{}
	teams map[string][]string
}

func (f *fakeApprovers) Permissions(_ context.Context, u string) (map[string]struct{}, error) {
	return f.perms[u], nil
}
func (f *fakeApprovers) TeamMemberIDs(_ context.Context, t string) ([]string, error) {
	return f.teams[t], nil
}
func (f *fakeApprovers) TeamIDsOfUser(context.Context, string) ([]string, error) { return nil, nil }

// fakeDir answers ActiveUsers: everybody is active unless listed in inactive.
type fakeDir struct {
	mu       sync.Mutex
	inactive map[string]bool
}

func (f *fakeDir) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = !f.inactive[id]
	}
	return out, nil
}

func (f *fakeDir) deactivate(id string) {
	f.mu.Lock()
	f.inactive[id] = true
	f.mu.Unlock()
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type env struct {
	t         *testing.T
	pool      *pgxpool.Pool
	svc       *application.Service
	store     *repository.Repository
	devices   *fakeDevices
	tickets   *fakeTickets
	holders   *fakeHolders
	approvals *fakeApprovals
	approvers *fakeApprovers
	dir       *fakeDir
	fake      *remoteaccess.Fake
	clk       *clock
	tech      application.Principal // a technician with start_attended
	admin     application.Principal // a remote_access.admin holder
}

func principal(id string, start, admin bool) application.Principal {
	return application.Principal{UserID: id, View: true, Start: start, Admin: admin}
}

func newEnv(t *testing.T, approvalOwnership ...string) *env {
	t.Helper()
	pool := dbtest.Pool(t)
	e := &env{t: t, pool: pool, store: repository.New(pool), devices: &fakeDevices{m: map[string]application.DeviceInfo{}},
		tickets: &fakeTickets{m: map[string]application.TicketInfo{}}, holders: &fakeHolders{m: map[string]string{}},
		approvals: &fakeApprovals{byID: map[string]*application.ApprovalInfo{}, bySubject: map[string][]string{}},
		approvers: &fakeApprovers{perms: map[string]map[string]struct{}{}, teams: map[string][]string{}},
		dir:       &fakeDir{inactive: map[string]bool{}}, fake: remoteaccess.NewFake(provider), clk: &clock{t: time.Now().UTC().Truncate(time.Millisecond)}}
	e.svc = application.NewService(e.store, e.devices, e.tickets, e.holders, e.dir, e.approvals, e.approvers, remoteaccess.NewRegistryOf(e.fake)).
		WithApprovalOwnership(approvalOwnership).WithClock(e.clk.now)
	e.tech = principal(newID(), true, false)
	e.admin = principal(newID(), false, true)
	e.approvers.perms[e.admin.UserID] = map[string]struct{}{application.PermAdmin: {}}
	e.approvers.perms[e.tech.UserID] = map[string]struct{}{"tickets.manage": {}}
	return e
}

func (e *env) caller(p application.Principal) application.Caller {
	return application.Caller{Actor: audit.UserActor(p.UserID), CorrelationID: "test-" + newID()}
}

// fixture creates a mapped, fresh Device, an open Ticket whose affected User holds the Device and returns the ids.
type fixture struct{ device, ticket, holder, peer string }

func (e *env) fixture(ownership string) fixture {
	e.t.Helper()
	f := fixture{device: newID(), ticket: newID(), holder: newID(), peer: newPeer()}
	asset := newID()
	e.devices.mu.Lock()
	e.devices.m[f.device] = application.DeviceInfo{ID: f.device, Name: "pc", AssetID: &asset, Ownership: ownership, ObservedAt: e.clk.now().Add(-time.Hour), LastCheckinAt: ptr(e.clk.now().Add(-time.Hour))}
	e.devices.mu.Unlock()
	e.holders.m[asset] = f.holder
	e.tickets.m[f.ticket] = application.TicketInfo{ID: f.ticket, Reference: "TKT-1", Open: true, AffectedUserID: f.holder, ReporterUserID: newID()}
	if _, err := e.svc.MapPeer(context.Background(), e.caller(e.admin), e.admin, f.device, provider, f.peer, "initial_mapping"); err != nil {
		e.t.Fatalf("map peer: %v", err)
	}
	return f
}

func (e *env) request(f fixture, p application.Principal) (application.Session, error) {
	return e.svc.Request(context.Background(), e.caller(p), p, application.NewSession{DeviceID: f.device, TicketID: f.ticket, Provider: provider})
}

func (e *env) mustRequest(f fixture) application.Session {
	e.t.Helper()
	s, err := e.request(f, e.tech)
	if err != nil {
		e.t.Fatalf("request: %v", err)
	}
	return s
}

func (e *env) launchAndExchange(s application.Session) (application.Exchanged, string) {
	e.t.Helper()
	ctx := context.Background()
	h, err := e.svc.Launch(ctx, e.caller(e.tech), e.tech, s.ID, &s.Version)
	if err != nil {
		e.t.Fatalf("launch: %v", err)
	}
	x, err := e.svc.Exchange(ctx, e.caller(e.tech), e.tech, h.Token)
	if err != nil {
		e.t.Fatalf("exchange: %v", err)
	}
	return x, h.Token
}

func (e *env) get(id string) application.Session {
	e.t.Helper()
	s, err := e.store.GetSession(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func (e *env) decide(sessionID, approvalID, decision string) error {
	payload, _ := json.Marshal(map[string]any{"approvalId": approvalID, "subjectType": application.SubjectType, "subjectId": sessionID, "decision": decision})
	return pgx.BeginFunc(context.Background(), e.pool, func(tx pgx.Tx) error {
		return e.svc.OnApprovalDecided(context.Background(), tx, events.OutboxEvent{ID: newID(), EventType: "ApprovalDecided", Payload: payload, CorrelationID: "test-" + newID()})
	})
}

type recordingNotifier struct{ intents []notifications.Intent }

func (r *recordingNotifier) Create(_ context.Context, _ pgx.Tx, in notifications.Intent) (bool, error) {
	r.intents = append(r.intents, in)
	return true, nil
}

func jobWithID(id string) jobs.Job { return jobs.Job{ID: id} }

func ptr[T any](v T) *T { return &v }
