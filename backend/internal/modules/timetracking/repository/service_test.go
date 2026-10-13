package repository_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/timetracking/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/timetracking/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// fakeTickets answers Ticket access from a table: user -> ticket -> access.
type fakeTickets struct {
	mu     sync.Mutex
	access map[string]map[string]application.TicketAccess
}

func (f *fakeTickets) grant(user, ticket string, a application.TicketAccess) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.access[user] == nil {
		f.access[user] = map[string]application.TicketAccess{}
	}
	a.Found = true
	f.access[user][ticket] = a
}

func (f *fakeTickets) Access(_ context.Context, user string, ids []string) (map[string]application.TicketAccess, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]application.TicketAccess{}
	for _, id := range ids {
		out[id] = f.access[user][id]
	}
	return out, nil
}

var _ = application.Tickets(&fakeTickets{})

func (f *fakeTickets) ViewQueues(_ context.Context, user string) (bool, []string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := map[string]bool{}
	var ids []string
	for _, a := range f.access[user] {
		if a.CanView && !seen[a.QueueID] {
			seen[a.QueueID] = true
			ids = append(ids, a.QueueID)
		}
	}
	return false, ids, nil
}

type fakeDir struct{ employees map[string]string }

func (d fakeDir) ActiveEmployees(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		_, ok := d.employees[id]
		out[id] = ok
	}
	return out, nil
}

func (d fakeDir) UserNames(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		out[id] = d.employees[id]
	}
	return out, nil
}

type env struct {
	t       *testing.T
	pool    *pgxpool.Pool
	svc     *application.Service
	tickets *fakeTickets
	now     time.Time
	queue   string
	ticket  string
	alice   application.Principal // log, works the ticket
	bob     application.Principal // log, works the ticket
	viewer  application.Principal // log, view-only access
	lead    application.Principal // manage, can view the ticket
	admin   application.Principal
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Pool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `DELETE FROM timetracking.time_entries; DELETE FROM timetracking.timers`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE timetracking.settings SET codetermination_basis=NULL, codetermination_recorded_on=NULL, dpia_recorded_on=NULL,
		edit_window_days=14, retention_months=24, show_entry_authors=true, min_report_group_size=3, version=1`); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, pool: pool, now: time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC), queue: newID(), ticket: newID(), tickets: &fakeTickets{access: map[string]map[string]application.TicketAccess{}}}
	mk := func(log, manage bool) application.Principal {
		return application.Principal{UserID: newID(), Log: log, Manage: manage}
	}
	e.alice, e.bob, e.viewer, e.lead = mk(true, false), mk(true, false), mk(true, false), mk(false, true)
	e.admin = application.Principal{UserID: newID(), Admin: true}
	work := application.TicketAccess{Reference: "T-1", QueueID: e.queue, CanView: true, CanWork: true}
	e.tickets.grant(e.alice.UserID, e.ticket, work)
	e.tickets.grant(e.bob.UserID, e.ticket, work)
	e.tickets.grant(e.viewer.UserID, e.ticket, application.TicketAccess{Reference: "T-1", QueueID: e.queue, CanView: true})
	e.tickets.grant(e.lead.UserID, e.ticket, application.TicketAccess{Reference: "T-1", QueueID: e.queue, CanView: true})
	dir := fakeDir{employees: map[string]string{e.alice.UserID: "Alice", e.bob.UserID: "Bob", e.viewer.UserID: "Vera", e.lead.UserID: "Lena"}}
	e.svc = application.NewService(repository.New(pool), e.tickets, dir).WithClock(func() time.Time { return e.now })
	return e
}

func (e *env) caller(p application.Principal) application.Caller {
	return application.Caller{Actor: audit.UserActor(p.UserID), CorrelationID: newID()}
}

func (e *env) day(offset int) string {
	return e.now.AddDate(0, 0, offset).Format(time.DateOnly)
}

func (e *env) entry(p application.Principal, minutes int, date string) application.NewEntry {
	return application.NewEntry{SubjectType: "ticket", SubjectID: e.ticket, WorkDate: date, DurationMinutes: minutes, Note: "checked cabling"}
}

func (e *env) create(p application.Principal, in application.NewEntry) application.Entry {
	e.t.Helper()
	out, err := e.svc.Create(context.Background(), e.caller(p), p, in)
	if err != nil {
		e.t.Fatalf("create: %v", err)
	}
	return out
}

func version(v int) *int { return &v }

func TestCreateOwnEntryAndAuditMinimisation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	en := e.create(e.alice, e.entry(e.alice, 90, e.day(0)))
	if en.UserID != e.alice.UserID || en.QueueID != e.queue || en.Source != "manual" || en.DurationMinutes != 90 || en.Version != 1 {
		t.Fatalf("entry = %+v", en)
	}
	var meta, after string
	if err := e.pool.QueryRow(ctx, `SELECT metadata::text, COALESCE(after_data::text, '') FROM platform.audit_events WHERE action = 'timetracking.entry.created' AND target_id = $1`, en.ID).Scan(&meta, &after); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(meta+after, "checked cabling") || strings.Contains(meta+after, "durationMinutes") || strings.Contains(meta, "reason") {
		t.Errorf("own entry audit carries content: meta=%s after=%s", meta, after)
	}
	if !strings.Contains(meta, e.ticket) {
		t.Errorf("audit lacks the subject: %s", meta)
	}
}

func TestCreateRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	other := e.entry(e.alice, 30, e.day(0))
	other.SubjectID = newID()
	cases := []struct {
		name string
		p    application.Principal
		in   application.NewEntry
		want error
	}{
		{"no permission", application.Principal{UserID: newID()}, e.entry(e.alice, 30, e.day(0)), application.ErrForbidden},
		{"view only queue access", e.viewer, e.entry(e.viewer, 30, e.day(0)), application.ErrForbidden},
		{"unknown ticket", e.alice, other, application.ErrNotFound},
		{"outside edit window", e.alice, e.entry(e.alice, 30, e.day(-15)), application.ErrOutsideEditWindow},
		{"zero minutes", e.alice, e.entry(e.alice, 0, e.day(0)), nil},
		{"too long", e.alice, e.entry(e.alice, 1441, e.day(0)), nil},
		{"far future", e.alice, e.entry(e.alice, 30, e.day(3)), nil},
		{"bad date", e.alice, e.entry(e.alice, 30, "12.10.2026"), nil},
	}
	for _, tc := range cases {
		_, err := e.svc.Create(ctx, e.caller(tc.p), tc.p, tc.in)
		var inv *application.InvalidInputError
		switch {
		case tc.want != nil && !errors.Is(err, tc.want):
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		case tc.want == nil && !errors.As(err, &inv):
			t.Errorf("%s: err = %v, want invalid input", tc.name, err)
		}
	}
	if _, err := e.svc.Create(ctx, e.caller(e.alice), e.alice, application.NewEntry{SubjectType: "ticket", SubjectID: e.ticket, WorkDate: e.day(1), DurationMinutes: 5}); err != nil {
		t.Errorf("a work date one day ahead (time zones ahead of UTC) must be accepted: %v", err)
	}
	long := e.entry(e.alice, 30, e.day(0))
	long.Note = strings.Repeat("x", 501)
	if _, err := e.svc.Create(ctx, e.caller(e.alice), e.alice, long); err == nil {
		t.Error("a note over 500 characters must be rejected")
	}
	long.Note = "bidi ‮ text"
	if _, err := e.svc.Create(ctx, e.caller(e.alice), e.alice, long); err == nil {
		t.Error("an unsafe note must be rejected")
	}
}

func TestDayLimitIsExactUnderConcurrency(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	date := e.day(-1)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var ok, limited int
	for range 12 { // 12 x 3 hours: only 8 fit into 24 hours
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.svc.Create(ctx, e.caller(e.alice), e.alice, e.entry(e.alice, 180, date))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, application.ErrDayLimit):
				limited++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok != 8 || limited != 4 {
		t.Fatalf("created %d, refused %d; want 8 and 4", ok, limited)
	}
	// A second person on the same date is not affected.
	if _, err := e.svc.Create(ctx, e.caller(e.bob), e.bob, e.entry(e.bob, 180, date)); err != nil {
		t.Fatalf("limit is per person: %v", err)
	}
}

func TestOnBehalfAndOutsideWindowNeedManageAndReason(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	in := e.entry(e.lead, 45, e.day(0))
	in.UserID = e.bob.UserID
	if _, err := e.svc.Create(ctx, e.caller(e.alice), e.alice, withUser(e.entry(e.alice, 45, e.day(0)), e.bob.UserID)); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("logging for somebody else without manage: %v", err)
	}
	if _, err := e.svc.Create(ctx, e.caller(e.lead), e.lead, in); err == nil || !strings.Contains(err.Error(), "reason") {
		t.Errorf("on behalf without a reason: %v", err)
	}
	in.Reason = "Bob was on call and asked me to log it"
	en, err := e.svc.Create(ctx, e.caller(e.lead), e.lead, in)
	if err != nil {
		t.Fatal(err)
	}
	if en.UserID != e.bob.UserID || en.CreatedBy != e.lead.UserID {
		t.Fatalf("entry = %+v", en)
	}
	var meta, after string
	if err := e.pool.QueryRow(ctx, `SELECT metadata::text, after_data::text FROM platform.audit_events WHERE action = 'timetracking.entry.created' AND target_id = $1`, en.ID).Scan(&meta, &after); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(meta, "on call") || !strings.Contains(after, e.bob.UserID) || !strings.Contains(after, "45") || strings.Contains(meta+after, "checked cabling") {
		t.Errorf("elevated audit: meta=%s after=%s", meta, after)
	}
	// Owner without work access, inactive owner, and an actor who cannot see the Ticket.
	ghost := withUser(in, e.viewer.UserID)
	if _, err := e.svc.Create(ctx, e.caller(e.lead), e.lead, ghost); err == nil || !strings.Contains(err.Error(), "work access") {
		t.Errorf("owner without work access: %v", err)
	}
	stranger := newID()
	e.tickets.grant(stranger, e.ticket, application.TicketAccess{QueueID: e.queue, CanView: true, CanWork: true})
	if _, err := e.svc.Create(ctx, e.caller(e.lead), e.lead, withUser(in, stranger)); err == nil || !strings.Contains(err.Error(), "active internal") {
		t.Errorf("owner who is no internal User: %v", err)
	}
	// Outside the window: manage with reason, a person without manage is refused.
	old := e.entry(e.lead, 20, e.day(-30))
	old.UserID, old.Reason = e.alice.UserID, "forgotten"
	if _, err := e.svc.Create(ctx, e.caller(e.lead), e.lead, old); err != nil {
		t.Fatalf("manage outside the window: %v", err)
	}
	if _, err := e.svc.Create(ctx, e.caller(e.alice), e.alice, e.entry(e.alice, 20, e.day(-30))); !errors.Is(err, application.ErrOutsideEditWindow) {
		t.Errorf("own entry outside the window: %v", err)
	}
}

func withUser(in application.NewEntry, user string) application.NewEntry {
	in.UserID = user
	return in
}

func TestUpdateAndDelete(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	en := e.create(e.alice, e.entry(e.alice, 60, e.day(-2)))
	change := application.EntryChange{SubjectID: e.ticket, WorkDate: e.day(-1), DurationMinutes: 75, Note: ""}

	if _, err := e.svc.Update(ctx, e.caller(e.alice), e.alice, en.ID, change, nil); err == nil {
		t.Error("expectedVersion is required")
	}
	if _, err := e.svc.Update(ctx, e.caller(e.alice), e.alice, en.ID, change, version(7)); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("stale version: %v", err)
	}
	if _, err := e.svc.Update(ctx, e.caller(e.bob), e.bob, en.ID, change, version(1)); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("another person's entry must look absent: %v", err)
	}
	if _, err := e.svc.Update(ctx, e.caller(e.viewer), application.Principal{UserID: e.viewer.UserID}, en.ID, change, version(1)); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("no time tracking permission: %v", err)
	}
	up, err := e.svc.Update(ctx, e.caller(e.alice), e.alice, en.ID, change, version(1))
	if err != nil {
		t.Fatal(err)
	}
	if up.DurationMinutes != 75 || up.WorkDate != e.day(-1) || up.Note != nil || up.Version != 2 {
		t.Fatalf("updated = %+v", up)
	}
	// Moving to a Ticket the person cannot work is refused; a workable one adopts the new Queue.
	moved := change
	moved.SubjectID = newID()
	if _, err := e.svc.Update(ctx, e.caller(e.alice), e.alice, en.ID, moved, version(2)); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("unknown target Ticket: %v", err)
	}
	e.tickets.grant(e.alice.UserID, moved.SubjectID, application.TicketAccess{QueueID: newID(), CanView: true})
	if _, err := e.svc.Update(ctx, e.caller(e.alice), e.alice, en.ID, moved, version(2)); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("view-only target Ticket: %v", err)
	}
	newQueue := newID()
	e.tickets.grant(e.alice.UserID, moved.SubjectID, application.TicketAccess{QueueID: newQueue, CanView: true, CanWork: true})
	mv, err := e.svc.Update(ctx, e.caller(e.alice), e.alice, en.ID, moved, version(2))
	if err != nil || mv.SubjectID != moved.SubjectID || mv.QueueID != newQueue {
		t.Fatalf("moved = %+v, %v", mv, err)
	}
	// An own edit inside the window is audited without content.
	var after string
	if err := e.pool.QueryRow(ctx, `SELECT COALESCE(after_data::text, '') FROM platform.audit_events WHERE action = 'timetracking.entry.updated' AND target_id = $1 ORDER BY occurred_at LIMIT 1`, en.ID).Scan(&after); err != nil || (after != "" && after != "null") {
		t.Errorf("own edit audit after_data = %q (%v)", after, err)
	}
	// Outside the window the person cannot change their own entry, manage can with a reason.
	e.now = e.now.AddDate(0, 0, 20)
	late := change
	late.SubjectID, late.WorkDate = moved.SubjectID, mv.WorkDate
	if _, err := e.svc.Update(ctx, e.caller(e.alice), e.alice, en.ID, late, version(3)); !errors.Is(err, application.ErrOutsideEditWindow) {
		t.Errorf("own edit outside the window: %v", err)
	}
	if err := e.svc.Delete(ctx, e.caller(e.alice), e.alice, en.ID, application.DeleteInput{DeleteReason: "other"}, version(3)); !errors.Is(err, application.ErrOutsideEditWindow) {
		t.Errorf("own delete outside the window: %v", err)
	}
	e.tickets.grant(e.lead.UserID, moved.SubjectID, application.TicketAccess{QueueID: newQueue, CanView: true})
	if err := e.svc.Delete(ctx, e.caller(e.lead), e.lead, en.ID, application.DeleteInput{DeleteReason: "duplicate"}, version(3)); err == nil {
		t.Error("manage deletion needs a reason")
	}
	if err := e.svc.Delete(ctx, e.caller(e.lead), e.lead, en.ID, application.DeleteInput{DeleteReason: "bogus", Reason: "cleanup"}, version(3)); err == nil {
		t.Error("closed delete reason")
	}
	if err := e.svc.Delete(ctx, e.caller(e.lead), e.lead, en.ID, application.DeleteInput{DeleteReason: "duplicate", Reason: "cleanup of old data"}, version(3)); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Delete(ctx, e.caller(e.lead), e.lead, en.ID, application.DeleteInput{DeleteReason: "duplicate", Reason: "cleanup of old data"}, version(4)); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("deleting twice: %v", err)
	}
	res, err := e.svc.Mine(ctx, e.alice, e.day(-40), e.day(0))
	if err != nil || len(res.Entries) != 0 {
		t.Errorf("deleted entries are excluded: %+v, %v", res, err)
	}
}

func TestDayLimitAppliesToMovesAndEdits(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	full := e.create(e.alice, e.entry(e.alice, 1000, e.day(-1)))
	small := e.create(e.alice, e.entry(e.alice, 500, e.day(-2)))
	ch := application.EntryChange{SubjectID: e.ticket, WorkDate: e.day(-1), DurationMinutes: 500}
	if _, err := e.svc.Update(ctx, e.caller(e.alice), e.alice, small.ID, ch, version(1)); !errors.Is(err, application.ErrDayLimit) {
		t.Errorf("moving onto a full day: %v", err)
	}
	ch = application.EntryChange{SubjectID: e.ticket, WorkDate: e.day(-1), DurationMinutes: 1440}
	if _, err := e.svc.Update(ctx, e.caller(e.alice), e.alice, full.ID, ch, version(1)); err != nil {
		t.Errorf("an entry does not count against itself: %v", err)
	}
}

func TestTicketPanelVisibility(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.create(e.alice, e.entry(e.alice, 60, e.day(0)))
	e.create(e.bob, e.entry(e.bob, 30, e.day(0)))

	tt, err := e.svc.ForTicket(ctx, e.alice, e.ticket)
	if err != nil {
		t.Fatal(err)
	}
	if tt.TotalMinutes != 90 || len(tt.Entries) != 2 || !tt.CanLog || tt.EditWindowDays != 14 {
		t.Fatalf("panel = %+v", tt)
	}
	for _, v := range tt.Entries {
		if v.UserName == "" || v.Own != (v.UserID == e.alice.UserID) || v.Editable != v.Own {
			t.Errorf("entry view = %+v", v)
		}
	}
	// View-only access sees the total only.
	vo, err := e.svc.ForTicket(ctx, e.viewer, e.ticket)
	if err != nil || vo.TotalMinutes != 90 || len(vo.Entries) != 0 || vo.CanLog {
		t.Errorf("view-only panel = %+v, %v", vo, err)
	}
	// Someone with no view access cannot even tell the Ticket exists.
	if _, err := e.svc.ForTicket(ctx, application.Principal{UserID: newID(), Log: true}, e.ticket); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("no view access: %v", err)
	}
	// Authors hidden: only own entries are listed, the rest is summed.
	if _, err := e.pool.Exec(ctx, `UPDATE timetracking.settings SET show_entry_authors = false`); err != nil {
		t.Fatal(err)
	}
	hidden, err := e.svc.ForTicket(ctx, e.alice, e.ticket)
	if err != nil {
		t.Fatal(err)
	}
	if len(hidden.Entries) != 1 || hidden.Entries[0].UserID != e.alice.UserID || hidden.OthersMinutes != 30 || hidden.TotalMinutes != 90 {
		t.Errorf("hidden authors panel = %+v", hidden)
	}
}

func TestMineScopesToCallerAndReferences(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.create(e.alice, e.entry(e.alice, 60, e.day(0)))
	e.create(e.bob, e.entry(e.bob, 30, e.day(0)))
	res, err := e.svc.Mine(ctx, e.alice, e.day(-7), e.day(0))
	if err != nil || len(res.Entries) != 1 || res.TotalMinutes != 60 || res.Entries[0].Reference != "T-1" {
		t.Fatalf("mine = %+v, %v", res, err)
	}
	// Access to the Ticket was withdrawn: the entry stays, the reference is not shown.
	e.tickets.grant(e.alice.UserID, e.ticket, application.TicketAccess{})
	res, _ = e.svc.Mine(ctx, e.alice, e.day(-7), e.day(0))
	if len(res.Entries) != 1 || res.Entries[0].Reference != "" {
		t.Errorf("reference after losing access = %+v", res.Entries)
	}
	for _, bad := range [][2]string{{e.day(0), e.day(-1)}, {e.day(-400), e.day(0)}, {"x", e.day(0)}} {
		if _, err := e.svc.Mine(ctx, e.alice, bad[0], bad[1]); err == nil {
			t.Errorf("period %v must be rejected", bad)
		}
	}
	if _, err := e.svc.Mine(ctx, application.Principal{UserID: newID()}, e.day(-1), e.day(0)); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("mine without permission: %v", err)
	}
}

func TestSettings(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cur, err := e.svc.Settings(ctx, e.admin)
	if err != nil || cur.CodeterminationRecorded() || cur.EditWindowDays != 14 || cur.RetentionMonths != 24 || !cur.ShowEntryAuthors || cur.MinReportGroupSize != 3 {
		t.Fatalf("defaults = %+v, %v", cur, err)
	}
	if _, err := e.svc.Settings(ctx, e.alice); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("settings without admin: %v", err)
	}
	in := application.SettingsInput{CodeterminationBasis: "agreement", CodeterminationRecordedOn: "2026-09-30", EditWindowDays: 7, RetentionMonths: 12, ShowEntryAuthors: false, MinReportGroupSize: 4}
	for name, bad := range map[string]func(*application.SettingsInput){
		"basis without date":   func(s *application.SettingsInput) { s.CodeterminationRecordedOn = "" },
		"unknown basis":        func(s *application.SettingsInput) { s.CodeterminationBasis = "yes" },
		"date without basis":   func(s *application.SettingsInput) { s.CodeterminationBasis = "" },
		"future agreement":     func(s *application.SettingsInput) { s.CodeterminationRecordedOn = "2027-01-01" },
		"window too long":      func(s *application.SettingsInput) { s.EditWindowDays = 91 },
		"retention too short":  func(s *application.SettingsInput) { s.RetentionMonths = 2 },
		"group size too small": func(s *application.SettingsInput) { s.MinReportGroupSize = 1 },
		"bad dpia date":        func(s *application.SettingsInput) { s.DPIARecordedOn = "soon" },
	} {
		c := in
		bad(&c)
		if _, err := e.svc.UpdateSettings(ctx, e.caller(e.admin), e.admin, c, version(1)); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
	if _, err := e.svc.UpdateSettings(ctx, e.caller(e.admin), e.admin, in, nil); err == nil {
		t.Error("expectedVersion is required")
	}
	if _, err := e.svc.UpdateSettings(ctx, e.caller(e.alice), e.alice, in, version(1)); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("update without admin: %v", err)
	}
	out, err := e.svc.UpdateSettings(ctx, e.caller(e.admin), e.admin, in, version(1))
	if err != nil || !out.CodeterminationRecorded() || out.Version != 2 || out.EditWindowDays != 7 || out.ShowEntryAuthors {
		t.Fatalf("updated = %+v, %v", out, err)
	}
	if _, err := e.svc.UpdateSettings(ctx, e.caller(e.admin), e.admin, in, version(1)); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("stale settings version: %v", err)
	}
	removed := in
	removed.CodeterminationBasis, removed.CodeterminationRecordedOn = "", ""
	if _, err := e.svc.UpdateSettings(ctx, e.caller(e.admin), e.admin, removed, version(2)); err == nil {
		t.Error("a recorded co-determination basis cannot be removed")
	}
	var after string
	if err := e.pool.QueryRow(ctx, `SELECT after_data::text FROM platform.audit_events WHERE action = 'timetracking.settings.updated' ORDER BY occurred_at DESC LIMIT 1`).Scan(&after); err != nil ||
		!strings.Contains(after, "agreement") || !strings.Contains(after, "editWindowDays") {
		t.Errorf("settings audit = %s (%v)", after, err)
	}
	// The edit window setting is applied: 7 days.
	if _, err := e.svc.Create(ctx, e.caller(e.alice), e.alice, e.entry(e.alice, 10, e.day(-8))); !errors.Is(err, application.ErrOutsideEditWindow) {
		t.Errorf("window of 7 days: %v", err)
	}
}

func TestPurgeRetention(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `UPDATE timetracking.settings SET retention_months = 3`); err != nil {
		t.Fatal(err)
	}
	keep := e.create(e.alice, e.entry(e.alice, 10, e.day(0)))
	old := e.create(e.alice, e.entry(e.alice, 10, e.day(-1)))
	deleted := e.create(e.alice, e.entry(e.alice, 10, e.day(-2)))
	recentlyDeleted := e.create(e.alice, e.entry(e.alice, 10, e.day(-3)))
	if err := e.svc.Delete(ctx, e.caller(e.alice), e.alice, deleted.ID, application.DeleteInput{DeleteReason: "duplicate"}, version(1)); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Delete(ctx, e.caller(e.alice), e.alice, recentlyDeleted.ID, application.DeleteInput{DeleteReason: "duplicate"}, version(1)); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE timetracking.time_entries SET work_date = (date '2026-10-12' - interval '4 months')::date WHERE id = '` + old.ID + `'`,
		`UPDATE timetracking.time_entries SET deleted_at = now() - interval '45 days' WHERE id = '` + deleted.ID + `'`,
		`INSERT INTO timetracking.timers (user_id, subject_type, subject_id, started_at) VALUES ('` + newID() + `', 'ticket', '` + e.ticket + `', now() - interval '40 days')`,
		`INSERT INTO timetracking.timers (user_id, subject_type, subject_id) VALUES ('` + newID() + `', 'ticket', '` + e.ticket + `')`,
	} {
		if _, err := e.pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	counts, err := e.svc.Purge(ctx, newID())
	if err != nil || counts.Entries != 1 || counts.Deleted != 1 || counts.Timers != 1 {
		t.Fatalf("purge = %+v, %v", counts, err)
	}
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM timetracking.time_entries WHERE id = ANY($1::uuid[])`, []string{keep.ID, recentlyDeleted.ID}).Scan(&n); err != nil || n != 2 {
		t.Errorf("kept rows = %d (%v)", n, err)
	}
	if again, err := e.svc.Purge(ctx, newID()); err != nil || again != (application.PurgeCounts{}) {
		t.Errorf("second purge = %+v, %v", again, err)
	}
	var meta string
	if err := e.pool.QueryRow(ctx, `SELECT metadata::text FROM platform.audit_events WHERE action = 'timetracking.purge.completed' ORDER BY occurred_at DESC LIMIT 1`).Scan(&meta); err != nil || strings.Contains(meta, e.alice.UserID) {
		t.Errorf("purge audit = %s (%v)", meta, err)
	}
}

func TestOwnEntryNeedsCurrentWorkAccess(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	en := e.create(e.alice, e.entry(e.alice, 60, e.day(-1)))
	e.tickets.grant(e.alice.UserID, e.ticket, application.TicketAccess{Reference: "T-1", QueueID: e.queue, CanView: true})
	change := application.EntryChange{SubjectID: e.ticket, WorkDate: e.day(-1), DurationMinutes: 30}
	if _, err := e.svc.Update(ctx, e.caller(e.alice), e.alice, en.ID, change, version(1)); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("update without work access: %v", err)
	}
	if err := e.svc.Delete(ctx, e.caller(e.alice), e.alice, en.ID, application.DeleteInput{DeleteReason: "other"}, version(1)); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("delete without work access: %v", err)
	}
}
