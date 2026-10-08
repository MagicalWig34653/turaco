package application

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

// Service performs Presence operations. Audit actions are presence.settings.updated, presence.settings.purged,
// presence.entry.created|rescheduled|location_changed|recurrence_changed|cancelled, presence.entries.detail_viewed,
// presence.team_minimum.set and presence.purge.completed. Audit and event payloads carry ids, kinds and counts
// only: never an entry's times of another person, a location name or anything resembling a reason.
type Service struct {
	store Store
	dir   Directory
	// enabled is PRESENCE_ENABLED, the startup gate; the runtime switch is Settings.Enabled.
	enabled      bool
	maxRetention int
	staleAfter   time.Duration
	now          func() time.Time
}

// Config is the installation configuration.
type Config struct {
	// Enabled is PRESENCE_ENABLED.
	Enabled bool
	// RetentionDays is PRESENCE_RETENTION_DAYS (1 to 30): the longest retention the runtime setting may reach.
	RetentionDays int
	// StaleAfter is PRESENCE_SOURCE_STALE_AFTER: external signals older than this count as unknown.
	StaleAfter time.Duration
}

// NewService wires the use cases.
func NewService(store Store, dir Directory, cfg Config) *Service {
	if cfg.RetentionDays < 1 || cfg.RetentionDays > 30 {
		cfg.RetentionDays = 30
	}
	if cfg.StaleAfter <= 0 {
		cfg.StaleAfter = DefaultStale
	}
	return &Service{store: store, dir: dir, enabled: cfg.Enabled, maxRetention: cfg.RetentionDays, staleAfter: cfg.StaleAfter,
		now: func() time.Time { return time.Now().UTC() }}
}

// WithClock replaces the clock (tests).
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

// ModuleEnabled reports the startup gate.
func (s *Service) ModuleEnabled() bool { return s.enabled }

var (
	uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	systemActor = audit.SystemActor("presence-retention")
)

func checkID(id string) (string, error) {
	if !uuidPattern.MatchString(id) {
		return "", invalid("ids must be UUIDs")
	}
	return strings.ToLower(id), nil
}

func requireVersion(expected *int) (int, error) {
	if expected == nil {
		return 0, invalid("expectedVersion is required")
	}
	return *expected, nil
}

func publish(ctx context.Context, tx pgx.Tx, c Caller, typ string, payload map[string]any) error {
	var actor *string
	if c.Actor.UserID != "" {
		a := c.Actor.UserID
		actor = &a
	}
	return events.Publish(ctx, tx, events.Publication{Type: typ, ActorID: actor, CorrelationID: c.CorrelationID, Payload: payload})
}

func recordAudit(ctx context.Context, tx pgx.Tx, c Caller, action, targetType, targetID string, before, after any, meta map[string]any) error {
	if len(meta) == 0 {
		meta = nil
	}
	return audit.Record(ctx, tx, audit.Change{Action: "presence." + action, TargetType: targetType, TargetID: targetID, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Before: before, After: after, Metadata: meta})
}

// active returns the settings when the module is switched on (startup gate and runtime switch).
func (s *Service) active(ctx context.Context) (Settings, error) {
	if !s.enabled {
		return Settings{}, ErrDisabled
	}
	st, err := s.store.GetSettings(ctx)
	if err != nil {
		return Settings{}, err
	}
	if !st.Enabled {
		return Settings{}, ErrDisabled
	}
	return st, nil
}

// scope is the set of Users a viewer may read availability of: the viewer and the current members of the
// viewer's Teams. Wider scopes need scoped role assignments, which the platform does not have yet.
func (s *Service) scope(ctx context.Context, userID string) ([]string, error) {
	out := []string{userID}
	seen := map[string]bool{userID: true}
	teams, err := s.dir.CurrentTeamIDs(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, t := range teams {
		members, err := s.dir.CurrentMemberIDs(ctx, t)
		if err != nil {
			return nil, err
		}
		for _, m := range members {
			if !seen[m] {
				seen[m] = true
				out = append(out, m)
			}
		}
	}
	return out, nil
}

func (s *Service) inScope(ctx context.Context, viewer, subject string) (bool, error) {
	if viewer == subject {
		return true, nil
	}
	scope, err := s.scope(ctx, viewer)
	if err != nil {
		return false, err
	}
	for _, id := range scope {
		if id == subject {
			return true, nil
		}
	}
	return false, nil
}

// checkWindow enforces the read window: at most 31 days, starting no earlier than today minus one day.
func (s *Service) checkWindow(from, to time.Time) (time.Time, time.Time, error) {
	from, to = from.UTC(), to.UTC()
	if !to.After(from) {
		return from, to, &WindowError{Message: "the window must end after it starts"}
	}
	if to.Sub(from) > MaxWindow {
		return from, to, &WindowError{Message: "the window spans at most 31 days"}
	}
	if from.Before(s.now().Add(-PastWindowSlack)) {
		return from, to, &WindowError{Message: "the window must not start more than one day in the past"}
	}
	return from, to, nil
}

// ---- Settings --------------------------------------------------------------------------------------------

// SettingsInput is the full replacement of the settings (every field is explicit).
type SettingsInput struct {
	Enabled                bool
	DPIARecordedOn         string // YYYY-MM-DD or empty
	CouncilConfirmedOn     string
	RetentionDays          int
	ExternalSourcesEnabled bool
}

// Settings returns the settings (presence.admin).
func (s *Service) Settings(ctx context.Context, p Principal) (Settings, error) {
	if !p.Admin {
		return Settings{}, ErrForbidden
	}
	return s.store.GetSettings(ctx)
}

// RetentionDays is the effective retention: the runtime setting capped by PRESENCE_RETENTION_DAYS.
func (s *Service) RetentionDays(st Settings) int {
	return min(st.RetentionDays, s.maxRetention)
}

func optDate(v string) (*string, error) {
	if v == "" {
		return nil, nil
	}
	if _, err := time.Parse(time.DateOnly, v); err != nil {
		return nil, invalid("dates must be in the form YYYY-MM-DD")
	}
	return &v, nil
}

// UpdateSettings replaces the settings (presence.admin). Enabling external sources needs the recorded data
// protection impact assessment date. Switching the module off starts the retention clock for all entries.
func (s *Service) UpdateSettings(ctx context.Context, c Caller, p Principal, in SettingsInput, expectedVersion *int) (Settings, error) {
	if !p.Admin {
		return Settings{}, ErrForbidden
	}
	want, err := requireVersion(expectedVersion)
	if err != nil {
		return Settings{}, err
	}
	dpia, err := optDate(in.DPIARecordedOn)
	if err != nil {
		return Settings{}, err
	}
	council, err := optDate(in.CouncilConfirmedOn)
	if err != nil {
		return Settings{}, err
	}
	if in.RetentionDays < 1 || in.RetentionDays > s.maxRetention {
		return Settings{}, invalid("retentionDays must be 1 to %d", s.maxRetention)
	}
	if in.ExternalSourcesEnabled && dpia == nil {
		return Settings{}, invalid("external sources need the recorded data protection impact assessment date")
	}
	var out Settings
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockSettingsTx(ctx, tx)
		if err != nil {
			return err
		}
		if cur.Version != want {
			return ErrVersionConflict
		}
		next := cur
		next.Enabled, next.DPIARecordedOn, next.CouncilConfirmedOn = in.Enabled, dpia, council
		next.RetentionDays, next.ExternalSourcesEnabled = in.RetentionDays, in.ExternalSourcesEnabled
		switch {
		case cur.Enabled && !in.Enabled:
			now := s.now()
			next.DisabledAt = &now
		case in.Enabled:
			next.DisabledAt = nil
		}
		if p.UserID != "" {
			next.UpdatedBy = &p.UserID
		}
		if out, err = s.store.UpdateSettingsTx(ctx, tx, next); err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "settings.updated", "presence_settings", "presence", settingsState(cur), settingsState(out), nil)
	})
	return out, err
}

// settingsState lists only switches, dates and counts.
func settingsState(st Settings) map[string]any {
	return map[string]any{"enabled": st.Enabled, "dpiaRecordedOn": st.DPIARecordedOn, "councilConfirmedOn": st.CouncilConfirmedOn,
		"retentionDays": st.RetentionDays, "externalSourcesEnabled": st.ExternalSourcesEnabled, "version": st.Version}
}

// PurgeNow deletes every entry at once (presence.admin; audited with the count).
func (s *Service) PurgeNow(ctx context.Context, c Caller, p Principal) (PurgeCounts, error) {
	if !p.Admin {
		return PurgeCounts{}, ErrForbidden
	}
	var counts PurgeCounts
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		var err error
		if counts, err = s.store.PurgeTx(ctx, tx, s.now(), true); err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "settings.purged", "presence_settings", "presence", nil, nil,
			map[string]any{"entries": counts.Entries, "immediate": true})
	})
	return counts, err
}

// ---- Entries ---------------------------------------------------------------------------------------------

// NewEntry creates a manual entry. UserID defaults to the actor.
type NewEntry struct {
	UserID       string
	Kind         string
	LocationType string
	LocationID   string
	Time         TimeInput
	Recurrence   *RecurrenceInput
	Visibility   string
}

func entryState(e Entry) map[string]any {
	return map[string]any{"status": e.Status, "kind": e.Kind, "recurring": e.Recurrence != nil, "version": e.Version}
}

func (s *Service) entryEvent(ctx context.Context, tx pgx.Tx, c Caller, op string, e Entry) error {
	return publish(ctx, tx, c, EventEntryChanged, map[string]any{"entryId": e.ID, "userId": e.UserID, "operation": op,
		"from": e.StartsAt.UTC().Format(time.RFC3339), "to": e.EndedAt.UTC().Format(time.RFC3339)})
}

// authorizeSubject checks that the actor may write entries of subject: own entries need manage_own, others
// manage_entries within the actor's scope.
func (s *Service) authorizeSubject(ctx context.Context, p Principal, subject string) error {
	if p.UserID == "" {
		return ErrForbidden
	}
	if subject == p.UserID {
		if !p.ManageOwn && !p.ManageEntries {
			return ErrForbidden
		}
		return nil
	}
	if !p.ManageEntries {
		return ErrForbidden
	}
	ok, err := s.inScope(ctx, p.UserID, subject)
	if err != nil {
		return err
	}
	if !ok {
		return ErrForbidden
	}
	return nil
}

// Create records a manual entry (operation Create).
func (s *Service) Create(ctx context.Context, c Caller, p Principal, in NewEntry) (Entry, error) {
	if _, err := s.active(ctx); err != nil {
		return Entry{}, err
	}
	subject := in.UserID
	if subject == "" {
		subject = p.UserID
	}
	subject, err := checkID(subject)
	if err != nil {
		return Entry{}, err
	}
	if err := s.authorizeSubject(ctx, p, subject); err != nil {
		return Entry{}, err
	}
	if in.Kind != KindWorkLocation && in.Kind != KindUnavailable {
		return Entry{}, invalid("kind must be work_location or unavailable")
	}
	locType, locID, err := s.checkLocation(ctx, in.Kind, in.LocationType, in.LocationID)
	if err != nil {
		return Entry{}, err
	}
	vis := in.Visibility
	if vis == "" {
		vis = VisibilityAvailability
	}
	if vis != VisibilityAvailability && vis != VisibilityDetail {
		return Entry{}, invalid("visibility must be availability or detail")
	}
	starts, ends, allDay, err := resolveTimes(in.Time)
	if err != nil {
		return Entry{}, err
	}
	rec, endedAt, err := buildRecurrence(in.Recurrence, in.Time.Timezone, starts, ends, allDay)
	if err != nil {
		return Entry{}, err
	}
	if err := s.checkHorizon(starts, endedAt); err != nil {
		return Entry{}, err
	}
	if active, err := s.dir.ActiveUsers(ctx, []string{subject}); err != nil {
		return Entry{}, err
	} else if !active[subject] {
		return Entry{}, invalid("the user is not an active user")
	}
	e := Entry{UserID: subject, Kind: in.Kind, LocationType: locType, LocationID: locID, StartsAt: starts, EndsAt: ends, AllDay: allDay,
		Recurrence: rec, Source: SourceManual, Status: StatusActive, Visibility: vis, EndedAt: endedAt, CreatedBy: &p.UserID}
	var out Entry
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		n, err := s.store.CountActiveEntriesTx(ctx, tx, subject)
		if err != nil {
			return err
		}
		if n >= MaxActiveEntries {
			return invalid("a user has at most %d active entries; cancel entries that are no longer needed", MaxActiveEntries)
		}
		if out, err = s.store.InsertEntryTx(ctx, tx, e); err != nil {
			return err
		}
		if err := recordAudit(ctx, tx, c, "entry.created", "presence_entry", out.ID, nil, entryState(out),
			map[string]any{"subjectIsActor": subject == p.UserID}); err != nil {
			return err
		}
		return s.entryEvent(ctx, tx, c, "created", out)
	})
	return out, err
}

func (s *Service) checkLocation(ctx context.Context, kind, locType, locID string) (*string, *string, error) {
	if kind == KindUnavailable {
		if locType != "" || locID != "" {
			return nil, nil, invalid("an unavailable entry has no location")
		}
		return nil, nil, nil
	}
	switch locType {
	case LocationRemote, LocationTravelling:
		if locID != "" {
			return nil, nil, invalid("only locationType location takes a locationId")
		}
		return &locType, nil, nil
	case LocationLocation:
		id, err := checkID(locID)
		if err != nil {
			return nil, nil, invalid("a location entry needs a locationId")
		}
		active, err := s.dir.ActiveLocations(ctx, []string{id})
		if err != nil {
			return nil, nil, err
		}
		if !active[id] {
			return nil, nil, invalid("the location does not exist or is not active")
		}
		return &locType, &id, nil
	}
	return nil, nil, invalid("a work location entry needs locationType location, remote or travelling")
}

// checkHorizon keeps entries inside the planning horizon: not already over for more than a day and starting at
// most 366 days ahead.
func (s *Service) checkHorizon(starts, endedAt time.Time) error {
	now := s.now()
	if endedAt.Before(now.Add(-PastWindowSlack)) {
		return invalid("an entry must not end more than one day in the past")
	}
	if starts.After(now.Add(MaxRecurrenceDays * 24 * time.Hour)) {
		return invalid("an entry starts at most 366 days ahead")
	}
	return nil
}

// loadForWrite locks an entry the actor may change. Entries of others the actor may not manage are not found.
func (s *Service) loadForWrite(ctx context.Context, tx pgx.Tx, p Principal, id string, expected *int, op string) (Entry, error) {
	id, err := checkID(id)
	if err != nil {
		return Entry{}, ErrNotFound
	}
	want, err := requireVersion(expected)
	if err != nil {
		return Entry{}, err
	}
	e, err := s.store.LockEntryTx(ctx, tx, id)
	if err != nil {
		return Entry{}, err
	}
	if aerr := s.authorizeSubject(ctx, p, e.UserID); aerr != nil {
		if errors.Is(aerr, ErrForbidden) && e.UserID != p.UserID {
			return Entry{}, ErrNotFound
		}
		return Entry{}, aerr
	}
	if e.Source != SourceManual {
		return Entry{}, ErrReadOnly
	}
	if e.Status != StatusActive {
		return Entry{}, &InvalidTransitionError{Operation: op, From: e.Status}
	}
	if e.Version != want {
		return Entry{}, ErrVersionConflict
	}
	return e, nil
}

func (s *Service) mutate(ctx context.Context, c Caller, p Principal, id string, expected *int, op, auditAction string,
	apply func(e Entry) (Entry, error)) (Entry, error) {
	if _, err := s.active(ctx); err != nil {
		return Entry{}, err
	}
	var out Entry
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.loadForWrite(ctx, tx, p, id, expected, op)
		if err != nil {
			return err
		}
		next, err := apply(cur)
		if err != nil {
			return err
		}
		if out, err = s.store.UpdateEntryTx(ctx, tx, next); err != nil {
			return err
		}
		if err := recordAudit(ctx, tx, c, auditAction, "presence_entry", out.ID, entryState(cur), entryState(out),
			map[string]any{"subjectIsActor": out.UserID == p.UserID}); err != nil {
			return err
		}
		return s.entryEvent(ctx, tx, c, op, out)
	})
	return out, err
}

// Reschedule moves an entry in time (operation Reschedule). A recurring entry keeps its rule and end date,
// re-anchored to the new first occurrence in its time zone.
func (s *Service) Reschedule(ctx context.Context, c Caller, p Principal, id string, expected *int, t TimeInput) (Entry, error) {
	return s.mutate(ctx, c, p, id, expected, "rescheduled", "entry.rescheduled", func(e Entry) (Entry, error) {
		if t.Timezone == "" && e.Recurrence != nil {
			t.Timezone = e.Recurrence.Timezone
		}
		starts, ends, allDay, err := resolveTimes(t)
		if err != nil {
			return Entry{}, err
		}
		e.StartsAt, e.EndsAt, e.AllDay = starts, ends, allDay
		e.EndedAt = ends
		if e.Recurrence != nil {
			r := RecurrenceInput{Frequency: e.Recurrence.Frequency, Interval: e.Recurrence.Interval, EndsOn: e.Recurrence.EndsOn}
			if e.Recurrence, e.EndedAt, err = buildRecurrence(&r, e.Recurrence.Timezone, starts, ends, allDay); err != nil {
				return Entry{}, err
			}
		}
		if err := s.checkHorizon(starts, e.EndedAt); err != nil {
			return Entry{}, err
		}
		return e, nil
	})
}

// ChangeLocation changes where a work location entry is (operation ChangeLocation).
func (s *Service) ChangeLocation(ctx context.Context, c Caller, p Principal, id string, expected *int, locType, locID string) (Entry, error) {
	return s.mutate(ctx, c, p, id, expected, "location_changed", "entry.location_changed", func(e Entry) (Entry, error) {
		if e.Kind != KindWorkLocation {
			return Entry{}, &InvalidTransitionError{Operation: "location_changed", From: e.Kind}
		}
		lt, li, err := s.checkLocation(ctx, KindWorkLocation, locType, locID)
		if err != nil {
			return Entry{}, err
		}
		e.LocationType, e.LocationID = lt, li
		return e, nil
	})
}

// ChangeRecurrence sets, replaces or (rec nil) removes the recurrence (operation ChangeRecurrence).
func (s *Service) ChangeRecurrence(ctx context.Context, c Caller, p Principal, id string, expected *int, timezone string, rec *RecurrenceInput) (Entry, error) {
	return s.mutate(ctx, c, p, id, expected, "recurrence_changed", "entry.recurrence_changed", func(e Entry) (Entry, error) {
		if rec == nil {
			e.Recurrence, e.EndedAt = nil, e.EndsAt
			return e, nil
		}
		tz := timezone
		if tz == "" && e.Recurrence != nil {
			tz = e.Recurrence.Timezone
		}
		var err error
		if e.Recurrence, e.EndedAt, err = buildRecurrence(rec, tz, e.StartsAt, e.EndsAt, e.AllDay); err != nil {
			return Entry{}, err
		}
		return e, s.checkHorizon(e.StartsAt, e.EndedAt)
	})
}

// Cancel ends an entry (operation Cancel, terminal). The entry stays until retention removes it.
func (s *Service) Cancel(ctx context.Context, c Caller, p Principal, id string, expected *int) (Entry, error) {
	return s.mutate(ctx, c, p, id, expected, "cancelled", "entry.cancelled", func(e Entry) (Entry, error) {
		now := s.now()
		e.Status, e.CancelledAt = StatusCancelled, &now
		if now.Before(e.EndedAt) {
			e.EndedAt = now
		}
		return e, nil
	})
}

// ---- Minimums --------------------------------------------------------------------------------------------

// MinimumInput sets the per-Team minimum.
type MinimumInput struct {
	Minimum       int
	OnsiteMinimum *int
	LocationID    string
}

// SetMinimum sets the minimum coverage of a Team (presence.manage_teams).
func (s *Service) SetMinimum(ctx context.Context, c Caller, p Principal, teamID string, in MinimumInput, expectedVersion *int) (Minimum, error) {
	if !p.ManageTeams {
		return Minimum{}, ErrForbidden
	}
	if _, err := s.active(ctx); err != nil {
		return Minimum{}, err
	}
	teamID, err := checkID(teamID)
	if err != nil {
		return Minimum{}, ErrNotFound
	}
	if in.Minimum < 0 || in.Minimum > 1000 || (in.OnsiteMinimum != nil && (*in.OnsiteMinimum < 0 || *in.OnsiteMinimum > 1000)) {
		return Minimum{}, invalid("minimums must be 0 to 1000")
	}
	teams, err := s.dir.ActiveTeams(ctx, []string{teamID})
	if err != nil {
		return Minimum{}, err
	}
	if !teams[teamID] {
		return Minimum{}, ErrNotFound
	}
	var loc *string
	if in.LocationID != "" {
		id, err := checkID(in.LocationID)
		if err != nil {
			return Minimum{}, invalid("locationId must be a UUID")
		}
		active, err := s.dir.ActiveLocations(ctx, []string{id})
		if err != nil {
			return Minimum{}, err
		}
		if !active[id] {
			return Minimum{}, invalid("the location does not exist or is not active")
		}
		loc = &id
	}
	m := Minimum{TeamID: teamID, Minimum: in.Minimum, OnsiteMinimum: in.OnsiteMinimum, LocationID: loc, UpdatedBy: &p.UserID}
	var out Minimum
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		if out, err = s.store.UpsertMinimumTx(ctx, tx, m, expectedVersion); err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "team_minimum.set", "presence_team_minimum", teamID, nil,
			map[string]any{"minimum": out.Minimum, "onsiteMinimum": out.OnsiteMinimum, "version": out.Version}, nil)
	})
	return out, err
}

// ---- Retention -------------------------------------------------------------------------------------------

// Purge deletes entries past retention (and everything once the module has been off for the retention period).
// It runs whether or not the module is enabled: retention must keep working after a switch-off. One audit
// summary with counts is written when something was deleted.
func (s *Service) Purge(ctx context.Context, correlationID string) (PurgeCounts, error) {
	st, err := s.store.GetSettings(ctx)
	if err != nil {
		return PurgeCounts{}, err
	}
	now := s.now()
	days := time.Duration(s.RetentionDays(st)) * 24 * time.Hour
	cutoff := now.Add(-days)
	everything := !st.Enabled && st.DisabledAt != nil && !st.DisabledAt.Add(days).After(now)
	var counts PurgeCounts
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		if counts, err = s.store.PurgeTx(ctx, tx, cutoff, everything); err != nil {
			return err
		}
		if counts.Entries == 0 {
			return nil
		}
		id, err := s.store.NewID(ctx, tx)
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, Caller{Actor: systemActor, CorrelationID: correlationID}, "purge.completed", "presence_purge", id, nil, nil,
			map[string]any{"entries": counts.Entries, "cutoff": cutoff.Format(time.RFC3339), "everything": everything})
	})
	return counts, err
}

// IsEnabled reports whether the module is on (startup gate and runtime switch).
func (s *Service) IsEnabled(ctx context.Context) bool {
	_, err := s.active(ctx)
	return err == nil
}

// MaxRetentionDays is PRESENCE_RETENTION_DAYS, the upper bound of the runtime retention setting.
func (s *Service) MaxRetentionDays() int { return s.maxRetention }

// GetMinimum returns the Team's minimum (presence.manage_teams or presence.view_availability for a member).
func (s *Service) GetMinimum(ctx context.Context, p Principal, teamID string) (*Minimum, error) {
	if _, err := s.active(ctx); err != nil {
		return nil, err
	}
	id, err := checkID(teamID)
	if err != nil {
		return nil, ErrNotFound
	}
	if !p.ManageTeams {
		if !p.ViewAvailability {
			return nil, ErrForbidden
		}
		teams, err := s.dir.CurrentTeamIDs(ctx, p.UserID)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(teams, id) {
			return nil, ErrNotFound
		}
	}
	m, ok, err := s.store.GetMinimum(ctx, id)
	if err != nil || !ok {
		return nil, err
	}
	return &m, nil
}

// HandlePurge is the job handler of presence.purge (daily, idempotent).
func (s *Service) HandlePurge(ctx context.Context, job jobs.Job) error {
	_, err := s.Purge(ctx, "job:"+job.ID)
	return err
}
