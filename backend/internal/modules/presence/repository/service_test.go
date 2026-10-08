package repository_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/presence/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/presence/public"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/presence/repository"
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

type fakeDir struct {
	mu        sync.Mutex
	teams     map[string][]string // team -> members
	locations map[string]bool
}

func (f *fakeDir) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}
func (f *fakeDir) ActiveTeams(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		_, ok := f.teams[id]
		out[id] = ok
	}
	return out, nil
}
func (f *fakeDir) ActiveLocations(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = f.locations[id]
	}
	return out, nil
}
func (f *fakeDir) CurrentTeamIDs(_ context.Context, u string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for t, ms := range f.teams {
		for _, m := range ms {
			if m == u {
				out = append(out, t)
			}
		}
	}
	return out, nil
}
func (f *fakeDir) CurrentMemberIDs(_ context.Context, t string) ([]string, error) {
	return append([]string(nil), f.teams[t]...), nil
}

type env struct {
	t     *testing.T
	pool  *pgxpool.Pool
	svc   *application.Service
	dir   *fakeDir
	now   time.Time
	team  string
	loc   string
	lead  application.Principal // view_availability + view_entries + manage_entries + manage_teams + manage_own
	alice application.Principal // manage_own only, in the team
	bob   application.Principal // manage_own + view_availability, in the team
	carol application.Principal // manage_own, outside the team
	admin application.Principal
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Pool(t)
	ctx := context.Background()
	// Each test starts with the module on and no data left by other runs.
	if _, err := pool.Exec(ctx, `SELECT presence.purge_entries(now(), true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM presence.team_coverage_minimums`); err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, pool: pool, now: time.Now().UTC().Truncate(time.Minute), team: newID(), loc: newID()}
	mk := func(manage, view, entries, manageEntries, teams bool) application.Principal {
		return application.Principal{UserID: newID(), ManageOwn: manage, ViewAvailability: view, ViewEntries: entries, ManageEntries: manageEntries, ManageTeams: teams}
	}
	e.lead = mk(true, true, true, true, true)
	e.alice = mk(true, false, false, false, false)
	e.bob = mk(true, true, false, false, false)
	e.carol = mk(true, true, false, false, false)
	e.admin = application.Principal{UserID: newID(), Admin: true}
	e.dir = &fakeDir{teams: map[string][]string{e.team: {e.lead.UserID, e.alice.UserID, e.bob.UserID}}, locations: map[string]bool{e.loc: true}}
	e.svc = application.NewService(repository.New(pool), e.dir, application.Config{Enabled: true, RetentionDays: 30}).WithClock(func() time.Time { return e.now })
	e.setEnabled(true)
	return e
}

func (e *env) caller() application.Caller {
	return application.Caller{Actor: audit.UserActor(e.admin.UserID), CorrelationID: newID()}
}

func (e *env) setEnabled(on bool) application.Settings {
	e.t.Helper()
	ctx := context.Background()
	cur, err := e.svc.Settings(ctx, e.admin)
	if err != nil {
		e.t.Fatal(err)
	}
	v := cur.Version
	s, err := e.svc.UpdateSettings(ctx, e.caller(), e.admin, application.SettingsInput{Enabled: on, RetentionDays: 30}, &v)
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func (e *env) create(p application.Principal, in application.NewEntry) application.Entry {
	e.t.Helper()
	out, err := e.svc.Create(context.Background(), application.Caller{Actor: audit.UserActor(p.UserID), CorrelationID: newID()}, p, in)
	if err != nil {
		e.t.Fatalf("create: %v", err)
	}
	return out
}

func (e *env) day(offsetDays int) (time.Time, time.Time) {
	d := e.now.Truncate(24*time.Hour).AddDate(0, 0, offsetDays)
	return d, d.Add(24 * time.Hour)
}

func (e *env) unavailable(p application.Principal, vis string, offset int) application.Entry {
	s, en := e.day(offset)
	return e.create(p, application.NewEntry{Kind: application.KindUnavailable, Time: application.TimeInput{StartsAt: &s, EndsAt: &en}, Visibility: vis})
}

func TestSchemaHasNoReasonColumns(t *testing.T) {
	pool := dbtest.Pool(t)
	rows, err := pool.Query(context.Background(), `SELECT table_name, column_name FROM information_schema.columns WHERE table_schema = 'presence'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	bad := regexp.MustCompile(`(?i)reason|note|comment|category|description|text|message|label|title|sick|health|leave|vacation|holiday|absence`)
	n := 0
	for rows.Next() {
		var table, col string
		if err := rows.Scan(&table, &col); err != nil {
			t.Fatal(err)
		}
		n++
		if bad.MatchString(col) {
			t.Errorf("presence.%s.%s looks like a reason or free text column", table, col)
		}
	}
	if n == 0 {
		t.Fatal("no presence columns found")
	}
	var text int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM information_schema.columns WHERE table_schema='presence'
		AND table_name='entries' AND data_type='text' AND column_name NOT IN ('kind','location_type','source','source_ref','status','visibility')`).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if text != 0 {
		t.Errorf("entries has %d unexpected free text columns", text)
	}
}

func TestNoReasonStoredOrAudited(t *testing.T) {
	e := newEnv(t)
	en := e.unavailable(e.alice, "", 1)
	ctx := context.Background()
	var meta string
	if err := e.pool.QueryRow(ctx, `SELECT coalesce(metadata::text,'') || coalesce(before_data::text,'') || coalesce(after_data::text,'')
		FROM platform.audit_events WHERE action = 'presence.entry.created' AND target_id = $1`, en.ID).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`(?i)reason|note|sick|vacation|holiday|location|starts|ends`).MatchString(meta) {
		t.Errorf("audit payload carries entry content: %s", meta)
	}
	var ev string
	if err := e.pool.QueryRow(ctx, `SELECT payload::text FROM platform.outbox_events WHERE payload->>'entryId' = $1 LIMIT 1`, en.ID).Scan(&ev); err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`(?i)kind|reason|location|unavailable`).MatchString(ev) {
		t.Errorf("event carries entry content: %s", ev)
	}
}

func TestOwnEntryLifecycleAndVersioning(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := application.Caller{Actor: audit.UserActor(e.alice.UserID), CorrelationID: newID()}
	s, en := e.day(2)
	created := e.create(e.alice, application.NewEntry{Kind: application.KindWorkLocation, LocationType: "remote", Time: application.TimeInput{StartsAt: &s, EndsAt: &en}})
	v := created.Version
	moved, err := e.svc.ChangeLocation(ctx, c, e.alice, created.ID, &v, application.LocationLocation, e.loc)
	if err != nil || moved.LocationID == nil || moved.Version != v+1 {
		t.Fatalf("change location: %+v %v", moved, err)
	}
	if _, err := e.svc.ChangeLocation(ctx, c, e.alice, created.ID, &v, "remote", ""); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("stale version = %v, want conflict", err)
	}
	if _, err := e.svc.Cancel(ctx, c, e.alice, created.ID, nil); err == nil {
		t.Error("expectedVersion is required")
	}
	v = moved.Version
	rs, re := s.AddDate(0, 0, 1), en.AddDate(0, 0, 1)
	resched, err := e.svc.Reschedule(ctx, c, e.alice, created.ID, &v, application.TimeInput{StartsAt: &rs, EndsAt: &re})
	if err != nil || !resched.StartsAt.Equal(rs) {
		t.Fatalf("reschedule: %v", err)
	}
	v = resched.Version
	cancelled, err := e.svc.Cancel(ctx, c, e.alice, created.ID, &v)
	if err != nil || cancelled.Status != application.StatusCancelled || cancelled.CancelledAt == nil {
		t.Fatalf("cancel: %+v %v", cancelled, err)
	}
	v = cancelled.Version
	var tr *application.InvalidTransitionError
	if _, err := e.svc.Cancel(ctx, c, e.alice, created.ID, &v); !errors.As(err, &tr) {
		t.Errorf("cancel is terminal: %v", err)
	}
	list, err := e.svc.MyEntries(ctx, e.alice, s, s.AddDate(0, 0, 7))
	if err != nil || len(list) != 0 {
		t.Errorf("cancelled entries are not listed: %v %v", list, err)
	}
	// Audit: create, change location, reschedule, cancel.
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM platform.audit_events WHERE target_id = $1 AND action LIKE 'presence.entry.%'`, created.ID).Scan(&n); err != nil || n != 4 {
		t.Errorf("audit rows = %d (%v), want 5", n, err)
	}
}

func TestConcurrentUpdatesOneWins(t *testing.T) {
	e := newEnv(t)
	en := e.unavailable(e.alice, "", 1)
	c := application.Caller{Actor: audit.UserActor(e.alice.UserID), CorrelationID: newID()}
	v := en.Version
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, conflict := 0, 0
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.svc.Cancel(context.Background(), c, e.alice, en.ID, &v)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				ok++
			} else if errors.Is(err, application.ErrVersionConflict) || errors.As(err, new(*application.InvalidTransitionError)) {
				conflict++
			} else {
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok != 1 || conflict != 5 {
		t.Errorf("ok=%d conflict=%d", ok, conflict)
	}
}

func TestAuthorizationMatrix(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s, en := e.day(1)
	in := func(user string) application.NewEntry {
		return application.NewEntry{UserID: user, Kind: application.KindUnavailable, Time: application.TimeInput{StartsAt: &s, EndsAt: &en}}
	}
	nobody := application.Principal{UserID: newID()}
	for name, tc := range map[string]struct {
		p    application.Principal
		user string
		want error
	}{
		"no permission, own":          {nobody, nobody.UserID, application.ErrForbidden},
		"manage_own, own":             {e.alice, e.alice.UserID, nil},
		"manage_own, other":           {e.alice, e.bob.UserID, application.ErrForbidden},
		"manage_entries, team member": {e.lead, e.alice.UserID, nil},
		"manage_entries, outsider":    {e.lead, e.carol.UserID, application.ErrForbidden},
	} {
		_, err := e.svc.Create(ctx, application.Caller{Actor: audit.UserActor(tc.p.UserID), CorrelationID: newID()}, tc.p, in(tc.user))
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	// Other users' entries are invisible (not found) to somebody who cannot manage them.
	aliceEntry := e.unavailable(e.alice, "", 3)
	v := aliceEntry.Version
	if _, err := e.svc.Cancel(ctx, e.caller(), e.bob, aliceEntry.ID, &v); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("bob cancelling alice's entry = %v, want not found", err)
	}
	if _, err := e.svc.Cancel(ctx, e.caller(), e.lead, aliceEntry.ID, &v); err != nil {
		t.Errorf("lead cancelling a team member's entry: %v", err)
	}
	if _, err := e.svc.Settings(ctx, e.lead); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("settings need admin: %v", err)
	}
	if _, err := e.svc.SetMinimum(ctx, e.caller(), e.bob, e.team, application.MinimumInput{Minimum: 1}, nil); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("minimum needs manage_teams: %v", err)
	}
}

func TestScopedAvailabilityNoLeaks(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.unavailable(e.alice, "", 0)
	e.unavailable(e.carol, "", 0)
	at := e.now
	// bob (view_availability, same team) sees alice.
	res, err := e.svc.Availability(ctx, e.bob, []string{e.alice.UserID}, at.Add(time.Minute), application.Need{})
	if err != nil || res[0].Value != application.Unavailable {
		t.Fatalf("in scope: %+v %v", res, err)
	}
	// bob must not see carol (outside his scope): unknown, but the neutral hint exists for the assignment warning.
	res, err = e.svc.Availability(ctx, e.bob, []string{e.carol.UserID}, at.Add(time.Minute), application.Need{})
	if err != nil || res[0].Value != application.Unknown || res[0].Explanation != application.ExplainOutOfScope || len(res[0].Sources) != 0 {
		t.Fatalf("out of scope leaks: %+v %v", res, err)
	}
	if !res[0].MayBeUnavailable {
		t.Error("the neutral may-be-unavailable hint is expected")
	}
	// alice has no view_availability: sees herself only.
	res, _ = e.svc.Availability(ctx, e.alice, []string{e.alice.UserID, e.bob.UserID}, at.Add(time.Minute), application.Need{})
	if res[0].Value != application.Unavailable || res[1].Value != application.Unknown || res[1].Explanation != application.ExplainOutOfScope {
		t.Errorf("without view_availability: %+v", res)
	}
	// The window read has the same scope.
	segs, err := e.svc.AvailabilityWindow(ctx, e.bob, e.carol.UserID, at, at.Add(48*time.Hour), application.Need{})
	if err != nil || len(segs) != 1 || segs[0].Value != application.Unknown || segs[0].Explanation != application.ExplainOutOfScope {
		t.Errorf("window leaks: %+v %v", segs, err)
	}
	// The repository filter holds even when the application passes a foreign id.
	repo := repository.New(e.pool)
	got, err := repo.EntriesInWindow(ctx, []string{e.bob.UserID}, []string{e.carol.UserID}, at.Add(-time.Hour), at.Add(48*time.Hour))
	if err != nil || len(got) != 0 {
		t.Errorf("repository scope filter: %v %v", got, err)
	}
}

func TestDetailVisibilityAndAudit(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s, en := e.day(1)
	e.unavailable(e.alice, "", 1)                         // availability only
	e.unavailable(e.bob, application.VisibilityDetail, 1) // owner allows detail
	from, to := s, en
	if _, err := e.svc.Entries(ctx, e.caller(), e.bob, application.EntryFilter{From: from, To: to}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("view_entries required: %v", err)
	}
	c := application.Caller{Actor: audit.UserActor(e.lead.UserID), CorrelationID: newID()}
	list, err := e.svc.Entries(ctx, c, e.lead, application.EntryFilter{From: from, To: to})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Entry.UserID != e.bob.UserID {
		t.Errorf("only bob's detail-visible entry may be returned: %+v", list)
	}
	if _, err := e.svc.Entries(ctx, c, e.lead, application.EntryFilter{UserID: e.carol.UserID, From: from, To: to}); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("outside scope = %v", err)
	}
	var meta string
	if err := e.pool.QueryRow(ctx, `SELECT metadata::text FROM platform.audit_events WHERE action = 'presence.entries.detail_viewed' AND correlation_id = $1`, c.CorrelationID).Scan(&meta); err != nil {
		t.Fatalf("detail read must be audited: %v", err)
	}
	if regexp.MustCompile(`(?i)kind|unavailable|` + e.bob.UserID).MatchString(meta) {
		t.Errorf("detail audit leaks content: %s", meta)
	}
}

func TestWindowLimits(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var win *application.WindowError
	if _, err := e.svc.MyEntries(ctx, e.alice, e.now, e.now.AddDate(0, 0, 32)); !errors.As(err, &win) {
		t.Errorf("32 days: %v", err)
	}
	if _, err := e.svc.MyEntries(ctx, e.alice, e.now.AddDate(0, 0, -5), e.now.AddDate(0, 0, 2)); !errors.As(err, &win) {
		t.Errorf("past window (no per-person history): %v", err)
	}
	if _, err := e.svc.MyEntries(ctx, e.alice, e.now.Add(-12*time.Hour), e.now.AddDate(0, 0, 31).Add(-12*time.Hour)); err != nil {
		t.Errorf("31 days from yesterday evening must pass: %v", err)
	}
	if _, err := e.svc.AvailabilityWindow(ctx, e.bob, e.alice.UserID, e.now, e.now.AddDate(0, 0, 40), application.Need{}); !errors.As(err, &win) {
		t.Errorf("availability window: %v", err)
	}
	if _, err := e.svc.TeamCoverage(ctx, e.bob, e.team, e.now, e.now.AddDate(0, 0, 40), ""); !errors.As(err, &win) {
		t.Errorf("coverage window: %v", err)
	}
}

func TestTeamCoverageCountsAndThreshold(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s, en := e.day(1)
	remote := application.NewEntry{Kind: application.KindWorkLocation, LocationType: "remote", Time: application.TimeInput{StartsAt: &s, EndsAt: &en}}
	e.create(e.alice, remote)
	e.create(e.bob, application.NewEntry{Kind: application.KindWorkLocation, LocationType: "location", LocationID: e.loc, Time: application.TimeInput{StartsAt: &s, EndsAt: &en}})
	e.unavailable(e.lead, application.VisibilityDetail, 1)
	two := 2
	if _, err := e.svc.SetMinimum(ctx, e.caller(), e.lead, e.team, application.MinimumInput{Minimum: 3, OnsiteMinimum: &two, LocationID: e.loc}, nil); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.TeamCoverage(ctx, e.bob, e.team, s, en.Add(time.Hour), "UTC")
	if err != nil {
		t.Fatal(err)
	}
	var day application.CoverageDay
	for _, d := range res.Days {
		if d.Date == s.Format(time.DateOnly) {
			day = d
		}
	}
	if day.Available != 2 || day.Unavailable != 1 || day.State != application.CoverageBelow || day.OnsiteAvailable == nil || *day.OnsiteAvailable != 1 {
		t.Errorf("day = %+v", day)
	}
	if len(day.UnavailableUserIDs) != 0 || res.State != application.CoverageBelow {
		t.Errorf("names must not appear without view_entries: %+v", day)
	}
	withNames, _ := e.svc.TeamCoverage(ctx, e.lead, e.team, s, en.Add(time.Hour), "UTC")
	found := false
	for _, d := range withNames.Days {
		if len(d.UnavailableUserIDs) == 1 && d.UnavailableUserIDs[0] == e.lead.UserID {
			found = true
		}
	}
	if !found {
		t.Error("view_entries holders see the names of detail-visible absences")
	}
	// Outsiders cannot read the Team; a Team of one reveals a person and reports unknown.
	if _, err := e.svc.TeamCoverage(ctx, e.carol, e.team, s, en, ""); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("outsider = %v", err)
	}
	solo := newID()
	e.dir.teams[solo] = []string{e.carol.UserID}
	r, err := e.svc.TeamCoverage(ctx, e.carol, solo, s, en, "")
	if err != nil || r.State != application.CoverageUnknown || len(r.Days) != 0 || r.Members != nil {
		t.Errorf("team of one: %+v %v", r, err)
	}
}

func TestDisabledModule(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.unavailable(e.alice, "", 1)
	e.setEnabled(false)
	s, en := e.day(1)
	if _, err := e.svc.Create(ctx, e.caller(), e.alice, application.NewEntry{Kind: application.KindUnavailable, Time: application.TimeInput{StartsAt: &s, EndsAt: &en}}); !errors.Is(err, application.ErrDisabled) {
		t.Errorf("create while disabled: %v", err)
	}
	if _, err := e.svc.MyEntries(ctx, e.alice, s, en); !errors.Is(err, application.ErrDisabled) {
		t.Errorf("read while disabled: %v", err)
	}
	// The public contract answers unknown/Disabled with no data.
	pub := public.New(e.svc)
	res, err := pub.Availability(ctx, public.Viewer{UserID: e.bob.UserID, ViewAvailability: true}, []string{e.alice.UserID}, s, public.Need{})
	if err != nil || len(res) != 1 || !res[0].Disabled || res[0].Value != public.Unknown || len(res[0].Sources) != 0 {
		t.Errorf("public contract: %+v %v", res, err)
	}
	if cov, err := pub.TeamCoverage(ctx, public.Viewer{UserID: e.bob.UserID, ViewAvailability: true}, e.team, s, en, ""); err != nil || !cov.Disabled || len(cov.Days) != 0 {
		t.Errorf("coverage: %+v %v", cov, err)
	}
	// A startup-gated module (PRESENCE_ENABLED=false) is off whatever the runtime switch says.
	e.setEnabled(true)
	off := application.NewService(repository.New(e.pool), e.dir, application.Config{Enabled: false})
	if off.IsEnabled(ctx) {
		t.Error("PRESENCE_ENABLED=false must disable the module")
	}
}

func TestPublicContractNeverReturnsNames(t *testing.T) {
	e := newEnv(t)
	e.unavailable(e.alice, application.VisibilityDetail, 1)
	s, en := e.day(1)
	res, err := public.New(e.svc).TeamCoverage(context.Background(), public.Viewer{UserID: e.lead.UserID, ViewAvailability: true, ViewEntries: true}, e.team, s, en, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range res.Days {
		if len(d.UnavailableUserIDs) != 0 {
			t.Errorf("public coverage must not name people: %+v", d)
		}
	}
}

func TestSettingsRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cur, _ := e.svc.Settings(ctx, e.admin)
	v := cur.Version
	for name, in := range map[string]application.SettingsInput{
		"retention 31":          {Enabled: true, RetentionDays: 31},
		"retention 0":           {Enabled: true, RetentionDays: 0},
		"external without dpia": {Enabled: true, RetentionDays: 30, ExternalSourcesEnabled: true},
		"bad date":              {Enabled: true, RetentionDays: 30, DPIARecordedOn: "01.02.2026"},
	} {
		if _, err := e.svc.UpdateSettings(ctx, e.caller(), e.admin, in, &v); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	if _, err := e.svc.UpdateSettings(ctx, e.caller(), e.admin, application.SettingsInput{Enabled: true, RetentionDays: 7, DPIARecordedOn: "2026-09-01", CouncilConfirmedOn: "2026-09-15"}, &v); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.UpdateSettings(ctx, e.caller(), e.admin, application.SettingsInput{Enabled: true, RetentionDays: 7}, &v); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("stale settings version: %v", err)
	}
	// The database caps retention too.
	if _, err := e.pool.Exec(ctx, `UPDATE presence.settings SET retention_days = 31`); err == nil {
		t.Error("database must cap retention at 30 days")
	}
	if _, err := e.pool.Exec(ctx, `UPDATE presence.settings SET external_sources_enabled = true, dpia_recorded_on = NULL`); err == nil {
		t.Error("database must refuse external sources without the DPIA date")
	}
}

func TestRetentionPurge(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	past := e.unavailable(e.alice, "", 1)
	// An entry that ended 40 days ago, a cancelled recent one and a recurring one that ended long ago.
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	old := e.unavailable(e.bob, "", 2)
	exec(`ALTER TABLE presence.entries DISABLE TRIGGER entries_frozen_identity`)
	exec(`UPDATE presence.entries SET starts_at = now() - interval '41 days', ends_at = now() - interval '40 days', ended_at = now() - interval '40 days' WHERE id = $1`, old.ID)
	exec(`ALTER TABLE presence.entries ENABLE TRIGGER entries_frozen_identity`)
	cancelled := e.unavailable(e.lead, "", 3)
	v := cancelled.Version
	if _, err := e.svc.Cancel(ctx, application.Caller{Actor: audit.UserActor(e.lead.UserID), CorrelationID: newID()}, e.lead, cancelled.ID, &v); err != nil {
		t.Fatal(err)
	}
	// Deleting directly is impossible; only the audited purge deletes.
	if _, err := e.pool.Exec(ctx, `DELETE FROM presence.entries WHERE id = $1`, old.ID); err == nil {
		t.Error("direct delete must be refused")
	}
	if _, err := e.pool.Exec(ctx, `TRUNCATE presence.entries`); err == nil {
		t.Error("truncate must be refused")
	}
	// Cancelled entries count as ended at cancellation.
	corr := newID()
	counts, err := e.svc.Purge(ctx, corr)
	if err != nil || counts.Entries != 1 {
		t.Fatalf("first purge = %+v %v, want only the 40-day-old entry", counts, err)
	}
	e.now = e.now.AddDate(0, 0, 40)
	counts, err = e.svc.Purge(ctx, newID())
	if err != nil || counts.Entries != 2 {
		t.Fatalf("purge after 40 days = %+v %v, want the cancelled and the ended entry", counts, err)
	}
	var meta string
	if err := e.pool.QueryRow(ctx, `SELECT metadata::text FROM platform.audit_events WHERE action = 'presence.purge.completed' AND correlation_id = $1`, corr).Scan(&meta); err != nil {
		t.Fatalf("purge summary: %v", err)
	}
	if regexp.MustCompile(past.ID + `|` + old.UserID).MatchString(meta) {
		t.Errorf("purge audit carries identifiers: %s", meta)
	}
	if again, _ := e.svc.Purge(ctx, newID()); again.Entries != 0 {
		t.Error("purge is idempotent")
	}
}

func TestPurgeAfterDisableAndImmediate(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.unavailable(e.alice, "", 5)
	e.setEnabled(false)
	if c, err := e.svc.Purge(ctx, newID()); err != nil || c.Entries != 0 {
		t.Fatalf("retention period not over: %+v %v", c, err)
	}
	e.now = e.now.AddDate(0, 0, 31)
	if c, err := e.svc.Purge(ctx, newID()); err != nil || c.Entries != 1 {
		t.Fatalf("disabled for the retention period deletes everything: %+v %v", c, err)
	}
	e.setEnabled(true)
	e.unavailable(e.alice, "", 5)
	if _, err := e.svc.PurgeNow(ctx, e.caller(), e.lead); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("immediate purge needs presence.admin: %v", err)
	}
	if c, err := e.svc.PurgeNow(ctx, e.caller(), e.admin); err != nil || c.Entries != 1 {
		t.Fatalf("immediate purge: %+v %v", c, err)
	}
}

func TestExternalEntriesAreReadOnlyAndFrozen(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id := newID()
	s, en := e.day(1)
	if _, err := e.pool.Exec(ctx, `INSERT INTO presence.entries(id, user_id, kind, starts_at, ends_at, ended_at, source, source_ref, observed_from, observed_at)
		VALUES ($1, $2, 'unavailable', $3, $4, $4, 'fake', 'ref-1', now(), now())`, id, e.alice.UserID, s, en); err != nil {
		t.Fatal(err)
	}
	v := 1
	if _, err := e.svc.Cancel(ctx, e.caller(), e.lead, id, &v); !errors.Is(err, application.ErrReadOnly) {
		t.Errorf("external entry cancel = %v, want read only", err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE presence.entries SET user_id = $2 WHERE id = $1`, id, e.bob.UserID); err == nil {
		t.Error("identity columns are frozen")
	}
}

func TestEntryLimits(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := application.Caller{Actor: audit.UserActor(e.alice.UserID), CorrelationID: newID()}
	s := e.now.Add(time.Hour)
	for name, in := range map[string]application.NewEntry{
		"unavailable with location": {Kind: "unavailable", LocationType: "remote", Time: application.TimeInput{StartsAt: &s, EndsAt: ptr(s.Add(time.Hour))}},
		"unknown kind (reason)":     {Kind: "sick", Time: application.TimeInput{StartsAt: &s, EndsAt: ptr(s.Add(time.Hour))}},
		"location without id":       {Kind: "work_location", LocationType: "location", Time: application.TimeInput{StartsAt: &s, EndsAt: ptr(s.Add(time.Hour))}},
		"unknown location":          {Kind: "work_location", LocationType: "location", LocationID: newID(), Time: application.TimeInput{StartsAt: &s, EndsAt: ptr(s.Add(time.Hour))}},
		"over 31 days":              {Kind: "unavailable", Time: application.TimeInput{StartsAt: &s, EndsAt: ptr(s.AddDate(0, 0, 32))}},
		"end before start":          {Kind: "unavailable", Time: application.TimeInput{StartsAt: &s, EndsAt: ptr(s.Add(-time.Hour))}},
		"far future":                {Kind: "unavailable", Time: application.TimeInput{StartsAt: ptr(s.AddDate(2, 0, 0)), EndsAt: ptr(s.AddDate(2, 0, 1))}},
		"long past":                 {Kind: "unavailable", Time: application.TimeInput{StartsAt: ptr(s.AddDate(0, 0, -10)), EndsAt: ptr(s.AddDate(0, 0, -9))}},
	} {
		if _, err := e.svc.Create(ctx, c, e.alice, in); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	var rec *application.InvalidRecurrenceError
	_, err := e.svc.Create(ctx, c, e.alice, application.NewEntry{Kind: "unavailable", Time: application.TimeInput{StartsAt: &s, EndsAt: ptr(s.Add(time.Hour)), Timezone: "UTC"},
		Recurrence: &application.RecurrenceInput{Frequency: "daily"}})
	if !errors.As(err, &rec) {
		t.Errorf("recurrence without end: %v", err)
	}
}

func ptr[T any](v T) *T { return &v }

func TestMinimumVersioning(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	m, err := e.svc.SetMinimum(ctx, e.caller(), e.lead, e.team, application.MinimumInput{Minimum: 2}, nil)
	if err != nil || m.Version != 1 {
		t.Fatalf("%+v %v", m, err)
	}
	if _, err := e.svc.SetMinimum(ctx, e.caller(), e.lead, e.team, application.MinimumInput{Minimum: 3}, nil); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("update without version: %v", err)
	}
	v := 1
	if m, err = e.svc.SetMinimum(ctx, e.caller(), e.lead, e.team, application.MinimumInput{Minimum: 3}, &v); err != nil || m.Version != 2 {
		t.Errorf("update: %+v %v", m, err)
	}
	if _, err := e.svc.SetMinimum(ctx, e.caller(), e.lead, newID(), application.MinimumInput{Minimum: 1}, nil); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("unknown team: %v", err)
	}
}
