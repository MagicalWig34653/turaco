package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	orgpublic "github.com/MagicalWig34653/turaco/backend/internal/modules/organization/public"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

type recordingNotifier struct {
	mu      sync.Mutex
	intents []notifications.Intent
}

func (n *recordingNotifier) Create(_ context.Context, _ pgx.Tx, in notifications.Intent) (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.intents = append(n.intents, in)
	return true, nil
}

// partyFix is a peopleFix with a manager caller and helpers for External Parties and accounts.
type partyFix struct {
	*peopleFix
	manage application.Caller // holds organization.external_parties.manage
}

func newPartyFix(t *testing.T) *partyFix {
	f := newPeopleFix(t)
	c := f.caller("")
	c.ExternalPartiesManage = true
	return &partyFix{peopleFix: f, manage: c}
}

func (f *partyFix) newParty(sponsor string) application.ExternalParty {
	f.t.Helper()
	f.n++
	p, err := f.repo.CreateExternalParty(context.Background(), f.manage, application.NewExternalPartyInput{Kind: "vendor", Name: fmt.Sprintf("%s Vendor %d", f.pfx, f.n), SponsorUserID: sponsor})
	if err != nil {
		f.t.Fatalf("create party: %v", err)
	}
	f.t.Cleanup(func() {
		ctx := context.Background()
		_, _ = f.pool.Exec(ctx, `DELETE FROM organization.users WHERE external_party_id = $1::uuid`, p.ID)
		_, _ = f.pool.Exec(ctx, `DELETE FROM organization.external_parties WHERE id = $1::uuid`, p.ID)
	})
	return p
}

func (f *partyFix) external(party string, name string, expires time.Time) application.User {
	f.t.Helper()
	email := f.pfx + "-" + strings.ToLower(name) + "@vendor.example.test"
	u, err := f.repo.CreateLocalUser(context.Background(), f.manage, application.NewUserInput{DisplayName: f.pfx + " " + name, PrimaryEmail: &email,
		AccountKind: application.AccountKindExternal, ExternalPartyID: &party, AccessExpiresAt: &expires})
	if err != nil {
		f.t.Fatalf("create external account: %v", err)
	}
	f.cleanupUser(u.ID)
	return u
}

func TestExternalPartyLifecycle(t *testing.T) {
	f := newPartyFix(t)
	ctx := context.Background()
	sponsor := f.local("Sponsor")
	party := f.newParty(sponsor.ID)
	if party.SponsorName == "" || !party.Active || party.Version != 1 {
		t.Fatalf("party = %+v", party)
	}
	// Names are unique, whatever the case.
	if _, err := f.repo.CreateExternalParty(ctx, f.manage, application.NewExternalPartyInput{Kind: "partner", Name: strings.ToUpper(party.Name), SponsorUserID: sponsor.ID}); !errors.Is(err, application.ErrPartyNameTaken) {
		t.Errorf("duplicate name: %v", err)
	}
	// The sponsor is an active employee: not an external account, not a deactivated user, not unknown.
	inactive := f.user(f.pfx+" Gone", "inactive")
	if _, err := f.repo.CreateExternalParty(ctx, f.manage, application.NewExternalPartyInput{Kind: "vendor", Name: f.pfx + " Other", SponsorUserID: inactive}); !errors.Is(err, application.ErrSponsorInvalid) {
		t.Errorf("inactive sponsor: %v", err)
	}
	if _, err := f.repo.CreateExternalParty(ctx, f.manage, application.NewExternalPartyInput{Kind: "vendor", Name: f.pfx + " Other", SponsorUserID: missingID}); !errors.Is(err, application.ErrSponsorInvalid) {
		t.Errorf("unknown sponsor: %v", err)
	}
	ext := f.external(party.ID, "Vera", time.Now().UTC().Add(10*24*time.Hour).Truncate(time.Microsecond))
	if _, err := f.repo.CreateExternalParty(ctx, f.manage, application.NewExternalPartyInput{Kind: "vendor", Name: f.pfx + " Other", SponsorUserID: ext.ID}); !errors.Is(err, application.ErrSponsorInvalid) {
		t.Errorf("external sponsor: %v", err)
	}
	// Updates are versioned and audited; an unchanged request does not bump the version.
	name := f.pfx + " Renamed"
	if _, err := f.repo.UpdateExternalParty(ctx, f.manage, party.ID, application.ExternalPartyChange{ExpectedVersion: 7, Name: &name}); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("version conflict: %v", err)
	}
	got, err := f.repo.UpdateExternalParty(ctx, f.manage, party.ID, application.ExternalPartyChange{ExpectedVersion: 1, Name: &name})
	if err != nil || got.Name != name || got.Version != 2 || got.AccountCount != 1 {
		t.Fatalf("update = %+v %v", got, err)
	}
	if same, err := f.repo.UpdateExternalParty(ctx, f.manage, party.ID, application.ExternalPartyChange{ExpectedVersion: 2, Name: &name}); err != nil || same.Version != 2 {
		t.Errorf("no-op update = %+v %v", same, err)
	}
	off := false
	if got, err = f.repo.UpdateExternalParty(ctx, f.manage, party.ID, application.ExternalPartyChange{ExpectedVersion: 2, Active: &off}); err != nil || got.Active {
		t.Fatalf("deactivate = %+v %v", got, err)
	}
	if acts := f.actions(party.ID); !hasAction(acts, "organization.external_party.created") || !hasAction(acts, "organization.external_party.updated") {
		t.Errorf("audit actions = %v", acts)
	}
	list, err := f.repo.ListExternalParties(ctx)
	if err != nil || len(list) == 0 {
		t.Fatalf("list = %v %v", list, err)
	}
	detail, accounts, err := f.repo.GetExternalParty(ctx, party.ID)
	if err != nil || detail.ID != party.ID || len(accounts) != 1 || accounts[0].UserID != ext.ID {
		t.Errorf("get = %+v %+v %v", detail, accounts, err)
	}
	if _, _, err := f.repo.GetExternalParty(ctx, missingID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("unknown party: %v", err)
	}
}

func TestCreateExternalAccountRules(t *testing.T) {
	f := newPartyFix(t)
	ctx := context.Background()
	party := f.newParty(f.local("Sponsor").ID)
	until := time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Microsecond)
	u := f.external(party.ID, "Eva", until)
	if u.AccountKind != "external" || u.ExternalPartyID == nil || *u.ExternalPartyID != party.ID || u.AccessExpiresAt == nil || !u.AccessExpiresAt.Equal(until) || u.Origin != "local" {
		t.Fatalf("account = %+v", u)
	}
	if acts := f.actions(u.ID); !hasAction(acts, "organization.user.created_local") {
		t.Errorf("audit = %v", acts)
	}
	// The kind never changes (database trigger).
	if _, err := f.pool.Exec(ctx, `UPDATE organization.users SET account_kind = 'employee', external_party_id = NULL, access_expires_at = NULL WHERE id = $1::uuid`, u.ID); err == nil {
		t.Error("account_kind must be immutable")
	}
	// An inactive party takes no new accounts; an unknown party is not found.
	off := false
	if _, err := f.repo.UpdateExternalParty(ctx, f.manage, party.ID, application.ExternalPartyChange{ExpectedVersion: 1, Active: &off}); err != nil {
		t.Fatal(err)
	}
	email := f.pfx + "-late@vendor.example.test"
	in := application.NewUserInput{DisplayName: f.pfx + " Late", PrimaryEmail: &email, AccountKind: "external", ExternalPartyID: &party.ID, AccessExpiresAt: &until}
	if _, err := f.repo.CreateLocalUser(ctx, f.manage, in); !errors.Is(err, application.ErrPartyInactive) {
		t.Errorf("inactive party: %v", err)
	}
	missing := missingID
	in.ExternalPartyID = &missing
	if _, err := f.repo.CreateLocalUser(ctx, f.manage, in); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("unknown party: %v", err)
	}
	// An external account without a party is refused by the database as well.
	if _, err := f.pool.Exec(ctx, `INSERT INTO organization.users(display_name, account_kind, access_expires_at) VALUES ('x', 'external', now() + interval '1 day')`); err == nil {
		t.Error("an external account without a party must be refused by the constraint")
	}
}

func TestExpireAccessDeactivatesAndIsIdempotent(t *testing.T) {
	f := newPartyFix(t)
	ctx := context.Background()
	party := f.newParty(f.local("Sponsor").ID)
	now := time.Now().UTC().Truncate(time.Microsecond)
	due := f.external(party.ID, "Due", now.Add(time.Hour))
	later := f.external(party.ID, "Later", now.Add(48*time.Hour))
	// The end of access of "Due" passes (set directly: creation refuses a date in the past).
	if _, err := f.pool.Exec(ctx, `UPDATE organization.users SET access_expires_at = now() - interval '1 minute' WHERE id = $1::uuid`, due.ID); err != nil {
		t.Fatal(err)
	}
	// Both accounts have a session; only the due one loses it.
	for _, id := range []string{due.ID, later.ID} {
		f.exec(`INSERT INTO platform.sessions(token_hash, user_id, auth_method, idle_expires_at, absolute_expires_at)
			VALUES (sha256(convert_to($1::text, 'UTF8')), $1::uuid, 'local', now() + interval '1 hour', now() + interval '2 hours')`,
			`DELETE FROM platform.sessions WHERE user_id = $1::uuid`, id)
	}
	n, err := f.repo.ExpireAccess(ctx, time.Now().UTC(), 100)
	if err != nil || n < 1 {
		t.Fatalf("expire = %d %v", n, err)
	}
	if got := f.reload(due.ID); got.Status != "inactive" || got.StatusSource != "platform" || got.Version != due.Version+1 {
		t.Errorf("due account = %+v", got)
	}
	if got := f.reload(later.ID); got.Status != "active" {
		t.Errorf("later account = %+v", got)
	}
	var live int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM platform.sessions WHERE user_id = $1::uuid AND revoked_at IS NULL`, due.ID).Scan(&live)
	if live != 0 {
		t.Errorf("sessions of the expired account: %d", live)
	}
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM platform.sessions WHERE user_id = $1::uuid AND revoked_at IS NULL`, later.ID).Scan(&live)
	if live != 1 {
		t.Errorf("sessions of the other account: %d", live)
	}
	var actor string
	if err := f.pool.QueryRow(ctx, `SELECT metadata->>'actor' FROM platform.audit_events WHERE target_id = $1 AND action = 'organization.user.access_expired'`, due.ID).Scan(&actor); err != nil || actor != "access-expiry" {
		t.Errorf("audit actor = %q %v", actor, err)
	}
	var events int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM platform.outbox_events WHERE event_type = 'AccessExpired' AND payload->>'userId' = $1`, due.ID).Scan(&events)
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE payload->>'userId' = $1`, due.ID)
	})
	if events != 1 {
		t.Errorf("AccessExpired events = %d", events)
	}
	// A second run finds nothing for the account.
	if _, err := f.repo.ExpireAccess(ctx, time.Now().UTC(), 100); err != nil {
		t.Fatal(err)
	}
	if acts := f.actions(due.ID); countAction(acts, "organization.user.access_expired") != 1 {
		t.Errorf("audit actions = %v", acts)
	}
	// Reactivation needs a new end of access first.
	if _, err := f.repo.ChangeStatus(ctx, f.caller(""), due.ID, f.reload(due.ID).Version, application.OpReactivate, "returned"); !errors.Is(err, application.ErrWrongState) {
		t.Errorf("reactivation past the end of access: %v", err)
	}
}

func countAction(list []string, want string) int {
	n := 0
	for _, a := range list {
		if a == want {
			n++
		}
	}
	return n
}

func TestRemindSponsorsOncePerLeadWindow(t *testing.T) {
	f := newPartyFix(t)
	ctx := context.Background()
	sponsor := f.local("Sponsor")
	party := f.newParty(sponsor.ID)
	now := time.Now().UTC().Truncate(time.Second)
	ten := f.external(party.ID, "Ten", now.Add(10*24*time.Hour))
	two := f.external(party.ID, "Two", now.Add(2*24*time.Hour))
	f.external(party.ID, "Far", now.Add(60*24*time.Hour))
	n := &recordingNotifier{}
	repo := f.repo.WithNotifier(n)
	if _, err := repo.RemindSponsors(ctx, now, application.AccessReminderLeadDays, 100); err != nil {
		t.Fatal(err)
	}
	byUser := map[string]notifications.Intent{}
	for _, in := range n.intents {
		if in.RecipientUserID != sponsor.ID {
			continue
		}
		if _, dup := byUser[in.LinkID]; dup {
			t.Errorf("user %s reminded twice in one run", in.LinkID)
		}
		byUser[in.LinkID] = in
	}
	if in := byUser[ten.ID]; in.Params["leadDays"] != 14 || in.Category != application.NotificationAccessExpiring || in.LinkType != application.LinkSponsoredAccount {
		t.Errorf("10 days: %+v", in)
	}
	if in := byUser[two.ID]; in.Params["leadDays"] != 3 {
		t.Errorf("2 days: %+v", in)
	}
	if len(byUser) != 2 {
		t.Errorf("reminded accounts = %d, want 2", len(byUser))
	}
	// The dedupe key is stable across runs and changes with the end date (an extension starts a new series).
	first := byUser[ten.ID].DedupeKey
	n.intents = nil
	if _, err := repo.RemindSponsors(ctx, now.Add(time.Minute), application.AccessReminderLeadDays, 100); err != nil {
		t.Fatal(err)
	}
	for _, in := range n.intents {
		if in.LinkID == ten.ID && in.DedupeKey != first {
			t.Errorf("dedupe key changed between runs: %q vs %q", in.DedupeKey, first)
		}
	}
	if _, err := f.repo.RemindSponsors(ctx, now, application.AccessReminderLeadDays, 100); err == nil {
		t.Error("reminders need a notifier")
	}
}

func TestExtendAccessBySponsorOrPermission(t *testing.T) {
	f := newPartyFix(t)
	ctx := context.Background()
	sponsor := f.local("Sponsor")
	stranger := f.local("Stranger")
	party := f.newParty(sponsor.ID)
	until := time.Now().UTC().Add(10 * 24 * time.Hour).Truncate(time.Microsecond)
	ext := f.external(party.ID, "Ext", until)
	later := until.Add(20 * 24 * time.Hour)
	// A user who is neither the sponsor nor holds the permission learns nothing: the same answer for unknown ids.
	for _, id := range []string{ext.ID, missingID} {
		if _, err := f.repo.ExtendAccess(ctx, f.caller(stranger.ID), id, 1, later, "contract_renewed"); !errors.Is(err, application.ErrForbidden) {
			t.Errorf("stranger on %s: %v", id, err)
		}
	}
	if _, err := f.repo.ExtendAccess(ctx, f.caller(sponsor.ID), ext.ID, ext.Version, later, "sponsor_request"); err != nil {
		t.Fatalf("sponsor: %v", err)
	}
	var meta string
	if err := f.pool.QueryRow(ctx, `SELECT metadata::text FROM platform.audit_events WHERE target_id = $1 AND action = 'organization.user.access_extended'`, ext.ID).Scan(&meta); err != nil || !strings.Contains(meta, `"bySponsor": true`) && !strings.Contains(meta, `"bySponsor":true`) {
		t.Errorf("audit metadata = %q %v", meta, err)
	}
	// The sponsor of another party cannot extend this account.
	other := f.newParty(stranger.ID)
	_ = other
	if _, err := f.repo.ExtendAccess(ctx, f.caller(stranger.ID), ext.ID, ext.Version+1, later.Add(time.Hour), "correction"); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("other sponsor: %v", err)
	}
	// An inactive party takes no extension.
	off := false
	if _, err := f.repo.UpdateExternalParty(ctx, f.manage, party.ID, application.ExternalPartyChange{ExpectedVersion: 1, Active: &off}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.repo.ExtendAccess(ctx, f.manage, ext.ID, ext.Version+1, later.Add(time.Hour), "correction"); !errors.Is(err, application.ErrPartyInactive) {
		t.Errorf("inactive party: %v", err)
	}
}

func TestExternalAccountsAreInactiveAtTheEndOfAccess(t *testing.T) {
	f := newPartyFix(t)
	ctx := context.Background()
	party := f.newParty(f.local("Sponsor").ID)
	ext := f.external(party.ID, "Soon", time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond))
	emp := f.local("Employee")
	access := orgpublic.NewUserAccess(f.repo)
	st, err := access.Account(ctx, ext.ID)
	if err != nil || !st.Active || !st.External {
		t.Fatalf("before the end: %+v %v", st, err)
	}
	if st, _ := access.Account(ctx, emp.ID); !st.Active || st.External {
		t.Errorf("employee: %+v", st)
	}
	if st, _ := access.Account(ctx, missingID); st.Active || st.External {
		t.Errorf("unknown: %+v", st)
	}
	// The end of access passes without the job having run.
	if _, err := f.pool.Exec(ctx, `UPDATE organization.users SET access_expires_at = now() - interval '1 second' WHERE id = $1::uuid`, ext.ID); err != nil {
		t.Fatal(err)
	}
	if st, _ := access.Account(ctx, ext.ID); st.Active || !st.External {
		t.Errorf("after the end: %+v", st)
	}
	if active, _ := access.IsActive(ctx, ext.ID); active {
		t.Error("IsActive must be false after the end of access")
	}
	var locked bool
	if err := f.pool.QueryRow(ctx, `SELECT true`).Scan(&locked); err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error {
		ok, err := f.repo.LockActiveUser(ctx, tx, ext.ID)
		locked = ok
		return err
	}); err != nil || locked {
		t.Errorf("a login session must not be created after the end of access: %v %v", locked, err)
	}
	if active, _ := f.repo.ActiveUsers(ctx, []string{ext.ID, emp.ID}); active[ext.ID] || !active[emp.ID] {
		t.Errorf("ActiveUsers = %v", active)
	}
	if ex, _ := f.repo.ActiveExternals(ctx, []string{ext.ID}); ex[ext.ID] {
		t.Errorf("ActiveExternals after the end of access = %v", ex)
	}
}

func TestExternalDirectoryQueries(t *testing.T) {
	f := newPartyFix(t)
	ctx := context.Background()
	party := f.newParty(f.local("Sponsor").ID)
	until := time.Now().UTC().Add(5 * 24 * time.Hour).Truncate(time.Microsecond)
	ext := f.external(party.ID, "Quentin", until)
	emp := f.local("Quentin Employee")
	subjects := orgpublic.NewAuthorizationSubjects(f.repo)
	if ok, err := subjects.IsExternalAccount(ctx, ext.ID); err != nil || !ok {
		t.Errorf("IsExternalAccount(external) = %v %v", ok, err)
	}
	if ok, _ := subjects.IsExternalAccount(ctx, emp.ID); ok {
		t.Error("an employee is not external")
	}
	if ok, err := subjects.IsExternalAccount(ctx, "not-a-uuid"); ok || err != nil {
		t.Errorf("malformed id = %v %v", ok, err)
	}
	wd := orgpublic.NewWorkDirectory(f.repo)
	active, err := wd.ActiveExternals(ctx, []string{ext.ID, emp.ID})
	if err != nil || !active[ext.ID] || active[emp.ID] {
		t.Errorf("ActiveExternals = %v %v", active, err)
	}
	found, err := wd.SearchExternals(ctx, "quentin", 10)
	if err != nil {
		t.Fatal(err)
	}
	hit := false
	for _, a := range found {
		if a.UserID == emp.ID {
			t.Error("an employee must not be found among external accounts")
		}
		hit = hit || a.UserID == ext.ID && a.PartyName == party.Name
	}
	if !hit {
		t.Errorf("search = %+v", found)
	}
	if res, _ := wd.SearchExternals(ctx, "q", 10); len(res) != 0 {
		t.Errorf("one character must not search: %+v", res)
	}
	if res, _ := wd.SearchExternals(ctx, "%%", 10); len(res) != 0 {
		t.Errorf("wildcards are literal: %+v", res)
	}
	// An inactive party hides its accounts from the directory.
	off := false
	if _, err := f.repo.UpdateExternalParty(ctx, f.manage, party.ID, application.ExternalPartyChange{ExpectedVersion: 1, Active: &off}); err != nil {
		t.Fatal(err)
	}
	if active, _ := wd.ActiveExternals(ctx, []string{ext.ID}); active[ext.ID] {
		t.Error("accounts of an inactive party are not active externals")
	}
}
