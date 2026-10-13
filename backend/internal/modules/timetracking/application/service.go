package application

import (
	"context"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// Service performs Time Tracking operations. Audit actions are timetracking.entry.created|updated|deleted,
// timetracking.settings.updated and timetracking.purge.completed. Own changes inside the edit window are audited with
// the entry id, subject and action only; changes for another person or outside the window also carry the owner, the
// before/after duration and work date and the reason. The note text is never audited. There are no domain events.
type Service struct {
	store   Store
	tickets Tickets
	dir     Directory
	now     func() time.Time
}

// NewService wires the use cases.
func NewService(store Store, tickets Tickets, dir Directory) *Service {
	return &Service{store: store, tickets: tickets, dir: dir, now: func() time.Time { return time.Now().UTC() }}
}

// WithClock replaces the clock (tests).
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

var (
	uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	systemActor = audit.SystemActor("timetracking-retention")
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

func recordAudit(ctx context.Context, tx pgx.Tx, c Caller, action, targetType, targetID string, before, after any, meta map[string]any) error {
	if len(meta) == 0 {
		meta = nil
	}
	return audit.Record(ctx, tx, audit.Change{Action: "timetracking." + action, TargetType: targetType, TargetID: targetID, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Before: before, After: after, Metadata: meta})
}

func (s *Service) today() time.Time {
	n := s.now().UTC()
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

func parseDate(v, name string) (time.Time, error) {
	t, err := time.Parse(time.DateOnly, v)
	if err != nil {
		return time.Time{}, invalid("%s must be a date in the form YYYY-MM-DD", name)
	}
	return t, nil
}

// checkWorkDate validates a work date and reports whether it lies outside the edit window. A date up to one day
// after today (UTC) is accepted so that a person in a time zone ahead of UTC can log on their own current day.
func (s *Service) checkWorkDate(v string, st Settings) (outside bool, err error) {
	d, err := parseDate(v, "workDate")
	if err != nil {
		return false, err
	}
	today := s.today()
	if d.After(today.AddDate(0, 0, futureWorkDateDay)) {
		return false, invalid("workDate must not be in the future")
	}
	return d.Before(today.AddDate(0, 0, -st.EditWindowDays)), nil
}

func cleanNote(n string) (*string, error) {
	n = strings.TrimSpace(n)
	if n == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(n) > MaxNoteLength {
		return nil, invalid("note must have at most %d characters", MaxNoteLength)
	}
	if safetext.ContainsUnsafe(n, false) {
		return nil, invalid("note contains characters that are not allowed")
	}
	return &n, nil
}

func cleanReason(r string) (string, error) {
	r = strings.TrimSpace(r)
	if utf8.RuneCountInString(r) < MinReasonLength || utf8.RuneCountInString(r) > MaxReasonLength {
		return "", invalid("reason must have %d to %d characters", MinReasonLength, MaxReasonLength)
	}
	if safetext.ContainsUnsafe(r, false) {
		return "", invalid("reason contains characters that are not allowed")
	}
	return r, nil
}

func checkDuration(m int) error {
	if m < 1 || m > MaxDayMinutes {
		return invalid("durationMinutes must be 1 to %d", MaxDayMinutes)
	}
	return nil
}

func (p Principal) mayLog() bool { return p.Log || p.Manage }

// ---- Settings --------------------------------------------------------------------------------------------

// SettingsInput is the full replacement of the settings.
type SettingsInput struct {
	CodeterminationBasis      string // agreement, not_applicable or empty (not recorded)
	CodeterminationRecordedOn string // YYYY-MM-DD, required with a basis
	DPIARecordedOn            string // YYYY-MM-DD or empty
	EditWindowDays            int
	RetentionMonths           int
	ShowEntryAuthors          bool
	MinReportGroupSize        int
}

// Settings returns the settings (timetracking.admin).
func (s *Service) Settings(ctx context.Context, p Principal) (Settings, error) {
	if !p.Admin {
		return Settings{}, ErrForbidden
	}
	return s.store.GetSettings(ctx)
}

// UpdateSettings replaces the settings (timetracking.admin). Recording the co-determination basis is the
// precondition for switching the module on; once recorded it can be changed but not removed.
func (s *Service) UpdateSettings(ctx context.Context, c Caller, p Principal, in SettingsInput, expectedVersion *int) (Settings, error) {
	if !p.Admin {
		return Settings{}, ErrForbidden
	}
	want, err := requireVersion(expectedVersion)
	if err != nil {
		return Settings{}, err
	}
	var basis, basisOn, dpia *string
	switch in.CodeterminationBasis {
	case "":
		if in.CodeterminationRecordedOn != "" {
			return Settings{}, invalid("codeterminationRecordedOn needs a codeterminationBasis")
		}
	case BasisAgreement, BasisNotApplicable:
		b := in.CodeterminationBasis
		basis = &b
		d, err := parseDate(in.CodeterminationRecordedOn, "codeterminationRecordedOn")
		if err != nil {
			return Settings{}, err
		}
		if d.After(s.today().AddDate(0, 0, futureWorkDateDay)) {
			return Settings{}, invalid("codeterminationRecordedOn must not be in the future")
		}
		basisOn = &in.CodeterminationRecordedOn
	default:
		return Settings{}, invalid("codeterminationBasis must be agreement or not_applicable")
	}
	if in.DPIARecordedOn != "" {
		if _, err := parseDate(in.DPIARecordedOn, "dpiaRecordedOn"); err != nil {
			return Settings{}, err
		}
		dpia = &in.DPIARecordedOn
	}
	if in.EditWindowDays < 1 || in.EditWindowDays > 90 {
		return Settings{}, invalid("editWindowDays must be 1 to 90")
	}
	if in.RetentionMonths < 3 || in.RetentionMonths > 120 {
		return Settings{}, invalid("retentionMonths must be 3 to 120")
	}
	if in.MinReportGroupSize < 2 || in.MinReportGroupSize > 20 {
		return Settings{}, invalid("minReportGroupSize must be 2 to 20")
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
		if cur.CodeterminationRecorded() && basis == nil {
			return invalid("a recorded co-determination basis cannot be removed")
		}
		next := cur
		next.CodeterminationBasis, next.CodeterminationRecordedOn, next.DPIARecordedOn = basis, basisOn, dpia
		next.EditWindowDays, next.RetentionMonths = in.EditWindowDays, in.RetentionMonths
		next.ShowEntryAuthors, next.MinReportGroupSize = in.ShowEntryAuthors, in.MinReportGroupSize
		if p.UserID != "" {
			next.UpdatedBy = &p.UserID
		}
		if out, err = s.store.UpdateSettingsTx(ctx, tx, next); err != nil {
			return err
		}
		return recordAudit(ctx, tx, c, "settings.updated", "timetracking_settings", "timetracking", settingsState(cur), settingsState(out), nil)
	})
	return out, err
}

func settingsState(st Settings) map[string]any {
	return map[string]any{"codeterminationBasis": st.CodeterminationBasis, "codeterminationRecordedOn": st.CodeterminationRecordedOn,
		"dpiaRecordedOn": st.DPIARecordedOn, "editWindowDays": st.EditWindowDays, "retentionMonths": st.RetentionMonths,
		"showEntryAuthors": st.ShowEntryAuthors, "minReportGroupSize": st.MinReportGroupSize, "version": st.Version}
}

// ---- Writes ----------------------------------------------------------------------------------------------

// NewEntry logs a manual entry. UserID defaults to the actor; another person needs timetracking.manage and a Reason.
type NewEntry struct {
	SubjectType     string
	SubjectID       string
	UserID          string
	WorkDate        string
	DurationMinutes int
	Note            string
	Reason          string

	// Set only by Stop: the entry is a timer entry and the running timer is removed in the same transaction.
	source    string
	startedAt *time.Time
	inTx      func(ctx context.Context, tx pgx.Tx) error
}

// accessFor loads the access of one User to one Ticket.
func (s *Service) accessFor(ctx context.Context, userID, ticketID string) (TicketAccess, error) {
	m, err := s.tickets.Access(ctx, userID, []string{ticketID})
	if err != nil {
		return TicketAccess{}, err
	}
	return m[ticketID], nil
}

// authorizeTicket checks the actor's and the owner's access to the Ticket. The owner must be able to work it; an
// actor who logs for somebody else must at least view it. A Ticket the actor cannot view is reported as not found.
func (s *Service) authorizeTicket(ctx context.Context, p Principal, ownerID, ticketID string) (TicketAccess, error) {
	actor, err := s.accessFor(ctx, p.UserID, ticketID)
	if err != nil {
		return TicketAccess{}, err
	}
	if !actor.Found || !actor.CanView {
		return TicketAccess{}, ErrNotFound
	}
	if ownerID == p.UserID {
		if !actor.CanWork {
			return TicketAccess{}, ErrForbidden
		}
		return actor, nil
	}
	owner, err := s.accessFor(ctx, ownerID, ticketID)
	if err != nil {
		return TicketAccess{}, err
	}
	if !owner.CanWork {
		return TicketAccess{}, invalid("the person has no work access to this Ticket")
	}
	return actor, nil
}

func (s *Service) checkDay(ctx context.Context, tx pgx.Tx, userID, workDate, excludeID string, add int) error {
	used, err := s.store.DayMinutesTx(ctx, tx, userID, workDate, excludeID)
	if err != nil {
		return err
	}
	if used+add > MaxDayMinutes {
		return ErrDayLimit
	}
	return nil
}

// lockDates takes the per-person-and-date locks in a stable order.
func (s *Service) lockDates(ctx context.Context, tx pgx.Tx, userID string, dates ...string) error {
	if len(dates) == 2 && dates[0] == dates[1] {
		dates = dates[:1]
	}
	if len(dates) == 2 && dates[0] > dates[1] {
		dates[0], dates[1] = dates[1], dates[0]
	}
	for _, d := range dates {
		if err := s.store.LockPersonDateTx(ctx, tx, userID, d); err != nil {
			return err
		}
	}
	return nil
}

// Create logs time on a Ticket (manual entry). Work date, duration, 24-hour rule and access are enforced here.
func (s *Service) Create(ctx context.Context, c Caller, p Principal, in NewEntry) (Entry, error) {
	if p.UserID == "" || !p.mayLog() {
		return Entry{}, ErrForbidden
	}
	if in.SubjectType != SubjectTicket {
		return Entry{}, invalid("subjectType must be ticket")
	}
	ticketID, err := checkID(in.SubjectID)
	if err != nil {
		return Entry{}, err
	}
	owner := p.UserID
	if in.UserID != "" {
		if owner, err = checkID(in.UserID); err != nil {
			return Entry{}, err
		}
	}
	onBehalf := owner != p.UserID
	if onBehalf && !p.Manage {
		return Entry{}, ErrForbidden
	}
	if err := checkDuration(in.DurationMinutes); err != nil {
		return Entry{}, err
	}
	note, err := cleanNote(in.Note)
	if err != nil {
		return Entry{}, err
	}
	st, err := s.store.GetSettings(ctx)
	if err != nil {
		return Entry{}, err
	}
	outside, err := s.checkWorkDate(in.WorkDate, st)
	if err != nil {
		return Entry{}, err
	}
	if outside && !p.Manage {
		return Entry{}, ErrOutsideEditWindow
	}
	elevated := onBehalf || outside
	reason := ""
	if elevated {
		if reason, err = cleanReason(in.Reason); err != nil {
			return Entry{}, err
		}
	}
	acc, err := s.authorizeTicket(ctx, p, owner, ticketID)
	if err != nil {
		return Entry{}, err
	}
	if onBehalf {
		ok, err := s.dir.ActiveEmployees(ctx, []string{owner})
		if err != nil {
			return Entry{}, err
		}
		if !ok[owner] {
			return Entry{}, invalid("the person must be an active internal User")
		}
	}
	var out Entry
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		if err := s.lockDates(ctx, tx, owner, in.WorkDate); err != nil {
			return err
		}
		if err := s.checkDay(ctx, tx, owner, in.WorkDate, "", in.DurationMinutes); err != nil {
			return err
		}
		var err error
		source := SourceManual
		if in.source != "" {
			source = in.source
		}
		out, err = s.store.InsertEntryTx(ctx, tx, Entry{SubjectType: SubjectTicket, SubjectID: ticketID, QueueID: acc.QueueID, UserID: owner,
			WorkDate: in.WorkDate, DurationMinutes: in.DurationMinutes, StartedAt: in.startedAt, Source: source, Note: note, CreatedBy: p.UserID})
		if err != nil {
			return err
		}
		if in.inTx != nil {
			if err := in.inTx(ctx, tx); err != nil {
				return err
			}
		}
		var after map[string]any
		meta := map[string]any{"subjectType": SubjectTicket, "subjectId": ticketID}
		if elevated {
			after = map[string]any{"userId": owner, "workDate": out.WorkDate, "durationMinutes": out.DurationMinutes}
			meta["reason"], meta["onBehalf"], meta["outsideEditWindow"] = reason, onBehalf, outside
		}
		return recordAudit(ctx, tx, c, "entry.created", "time_entry", out.ID, nil, after, meta)
	})
	return out, err
}

// EntryChange is the full replacement of the editable fields of an entry.
type EntryChange struct {
	SubjectID       string
	WorkDate        string
	DurationMinutes int
	Note            string
	Reason          string
}

// authorizeEntry decides how the actor may change an entry: own entries of a person with timetracking.log inside the
// edit window without a reason; everything else needs timetracking.manage and a reason. Entries of other people are
// invisible (not found) to people without timetracking.manage.
func (s *Service) authorizeEntry(p Principal, e Entry, st Settings, newDate string) (elevated, outside bool, err error) {
	own := e.UserID == p.UserID
	if !p.mayLog() {
		return false, false, ErrForbidden
	}
	if !own && !p.Manage {
		return false, false, ErrNotFound
	}
	// A stored work date is always valid; only the window matters here.
	outside, _ = s.checkWorkDate(e.WorkDate, st)
	if newDate != "" {
		o2, err := s.checkWorkDate(newDate, st)
		if err != nil {
			return false, false, err
		}
		outside = outside || o2
	}
	if outside && !p.Manage {
		return false, false, ErrOutsideEditWindow
	}
	return !own || outside, outside, nil
}

// Update corrects an entry: duration, work date, note and Ticket. A corrected Ticket must again be workable by the owner.
func (s *Service) Update(ctx context.Context, c Caller, p Principal, id string, in EntryChange, expectedVersion *int) (Entry, error) {
	if p.UserID == "" {
		return Entry{}, ErrForbidden
	}
	id, err := checkID(id)
	if err != nil {
		return Entry{}, ErrNotFound
	}
	want, err := requireVersion(expectedVersion)
	if err != nil {
		return Entry{}, err
	}
	ticketID, err := checkID(in.SubjectID)
	if err != nil {
		return Entry{}, err
	}
	if err := checkDuration(in.DurationMinutes); err != nil {
		return Entry{}, err
	}
	note, err := cleanNote(in.Note)
	if err != nil {
		return Entry{}, err
	}
	st, err := s.store.GetSettings(ctx)
	if err != nil {
		return Entry{}, err
	}
	var out Entry
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockEntryTx(ctx, tx, id)
		if err != nil {
			return err
		}
		elevated, outside, err := s.authorizeEntry(p, cur, st, in.WorkDate)
		if err != nil {
			return err
		}
		if cur.Version != want {
			return ErrVersionConflict
		}
		reason := ""
		if elevated {
			if reason, err = cleanReason(in.Reason); err != nil {
				return err
			}
		}
		next := cur
		if cur.UserID != p.UserID || !p.Manage {
			// Somebody else's entry: the actor must see the Ticket it is on (and the new one). Own entries need
			// current work access to the Ticket unless the actor manages time entries.
			if _, err := s.authorizeTicket(ctx, p, cur.UserID, cur.SubjectID); err != nil {
				return err
			}
		}
		if ticketID != cur.SubjectID {
			acc, err := s.authorizeTicket(ctx, p, cur.UserID, ticketID)
			if err != nil {
				return err
			}
			next.SubjectID, next.QueueID = ticketID, acc.QueueID
		}
		if err := s.lockDates(ctx, tx, cur.UserID, cur.WorkDate, in.WorkDate); err != nil {
			return err
		}
		if err := s.checkDay(ctx, tx, cur.UserID, in.WorkDate, cur.ID, in.DurationMinutes); err != nil {
			return err
		}
		next.WorkDate, next.DurationMinutes, next.Note = in.WorkDate, in.DurationMinutes, note
		if out, err = s.store.UpdateEntryTx(ctx, tx, next); err != nil {
			return err
		}
		var before, after map[string]any
		meta := map[string]any{"subjectType": SubjectTicket, "subjectId": out.SubjectID}
		if elevated {
			before = map[string]any{"userId": cur.UserID, "subjectId": cur.SubjectID, "workDate": cur.WorkDate, "durationMinutes": cur.DurationMinutes}
			after = map[string]any{"userId": out.UserID, "subjectId": out.SubjectID, "workDate": out.WorkDate, "durationMinutes": out.DurationMinutes}
			meta["reason"], meta["onBehalf"], meta["outsideEditWindow"] = reason, cur.UserID != p.UserID, outside
		}
		return recordAudit(ctx, tx, c, "entry.updated", "time_entry", out.ID, before, after, meta)
	})
	return out, err
}

// DeleteInput deletes (soft) an entry with a closed reason.
type DeleteInput struct {
	DeleteReason string
	Reason       string // free text, required when the deletion needs timetracking.manage
}

// Delete soft-deletes an entry; it disappears from every read and is purged by retention.
func (s *Service) Delete(ctx context.Context, c Caller, p Principal, id string, in DeleteInput, expectedVersion *int) error {
	if p.UserID == "" {
		return ErrForbidden
	}
	id, err := checkID(id)
	if err != nil {
		return ErrNotFound
	}
	want, err := requireVersion(expectedVersion)
	if err != nil {
		return err
	}
	switch in.DeleteReason {
	case DeleteWrongTicket, DeleteDuplicate, DeleteWrongDuration, DeleteOther:
	default:
		return invalid("deleteReason must be one of wrong_ticket, duplicate, wrong_duration, other")
	}
	st, err := s.store.GetSettings(ctx)
	if err != nil {
		return err
	}
	return s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockEntryTx(ctx, tx, id)
		if err != nil {
			return err
		}
		elevated, outside, err := s.authorizeEntry(p, cur, st, "")
		if err != nil {
			return err
		}
		if cur.Version != want {
			return ErrVersionConflict
		}
		reason := ""
		if elevated {
			if reason, err = cleanReason(in.Reason); err != nil {
				return err
			}
		}
		if cur.UserID != p.UserID || !p.Manage {
			if _, err := s.authorizeTicket(ctx, p, cur.UserID, cur.SubjectID); err != nil {
				return err
			}
		}
		if err := s.store.SoftDeleteTx(ctx, tx, cur.ID, p.UserID, in.DeleteReason); err != nil {
			return err
		}
		var before map[string]any
		meta := map[string]any{"subjectType": SubjectTicket, "subjectId": cur.SubjectID, "deleteReason": in.DeleteReason}
		if elevated {
			before = map[string]any{"userId": cur.UserID, "workDate": cur.WorkDate, "durationMinutes": cur.DurationMinutes}
			meta["reason"], meta["onBehalf"], meta["outsideEditWindow"] = reason, cur.UserID != p.UserID, outside
		}
		return recordAudit(ctx, tx, c, "entry.deleted", "time_entry", cur.ID, before, nil, meta)
	})
}

// ---- Reads -----------------------------------------------------------------------------------------------

// EntryView is an entry as shown to one reader.
type EntryView struct {
	Entry
	UserName string // empty when authors are hidden
	Own      bool
	Editable bool
	// Reference is the Ticket's display number as the reader may know it (My time only).
	Reference string
}

// TicketTime is the time panel of one Ticket.
type TicketTime struct {
	TotalMinutes int
	// Entries is empty for readers who may not see entries (view-only Queue access, no Time Tracking permission).
	Entries []EntryView
	// OthersMinutes sums the entries of other people that are not listed because show_entry_authors is off.
	OthersMinutes    int
	CanLog           bool
	EditWindowDays   int
	ShowEntryAuthors bool
}

// editable tells whether the reader may change the entry (own inside the window, or timetracking.manage).
func (s *Service) editable(p Principal, e Entry, st Settings) bool {
	if p.Manage {
		return true
	}
	if !p.Log || e.UserID != p.UserID {
		return false
	}
	outside, err := s.checkWorkDate(e.WorkDate, st)
	return err == nil && !outside
}

// ForTicket returns the Ticket total and, to people who may work the Ticket and hold timetracking.log or manage,
// the entries. The Ticket must be viewable by the reader (otherwise not found).
func (s *Service) ForTicket(ctx context.Context, p Principal, ticketID string) (TicketTime, error) {
	if p.UserID == "" {
		return TicketTime{}, ErrForbidden
	}
	id, err := checkID(ticketID)
	if err != nil {
		return TicketTime{}, ErrNotFound
	}
	acc, err := s.accessFor(ctx, p.UserID, id)
	if err != nil {
		return TicketTime{}, err
	}
	if !acc.Found || !acc.CanView {
		return TicketTime{}, ErrNotFound
	}
	st, err := s.store.GetSettings(ctx)
	if err != nil {
		return TicketTime{}, err
	}
	total, err := s.store.SubjectTotal(ctx, SubjectTicket, id)
	if err != nil {
		return TicketTime{}, err
	}
	out := TicketTime{TotalMinutes: total, Entries: []EntryView{}, EditWindowDays: st.EditWindowDays, ShowEntryAuthors: st.ShowEntryAuthors,
		CanLog: acc.CanWork && p.mayLog()}
	if !out.CanLog {
		return out, nil
	}
	entries, err := s.store.ListBySubject(ctx, SubjectTicket, id)
	if err != nil {
		return TicketTime{}, err
	}
	var names map[string]string
	if st.ShowEntryAuthors && len(entries) > 0 {
		ids := make([]string, 0, len(entries))
		for _, e := range entries {
			ids = append(ids, e.UserID)
		}
		if names, err = s.dir.UserNames(ctx, ids); err != nil {
			return TicketTime{}, err
		}
	}
	for _, e := range entries {
		own := e.UserID == p.UserID
		if !own && !st.ShowEntryAuthors {
			out.OthersMinutes += e.DurationMinutes
			continue
		}
		v := EntryView{Entry: e, Own: own, Editable: s.editable(p, e, st)}
		if st.ShowEntryAuthors {
			v.UserName = names[e.UserID]
		}
		out.Entries = append(out.Entries, v)
	}
	return out, nil
}

// MyTime is the own-time read of a period.
type MyTime struct {
	Entries        []EntryView
	TotalMinutes   int
	EditWindowDays int
}

// Mine returns the caller's own entries between two dates (inclusive), newest first. References are shown only for
// Tickets the caller may still view.
func (s *Service) Mine(ctx context.Context, p Principal, from, to string) (MyTime, error) {
	if p.UserID == "" || !p.mayLog() {
		return MyTime{}, ErrForbidden
	}
	f, err := parseDate(from, "from")
	if err != nil {
		return MyTime{}, err
	}
	t, err := parseDate(to, "to")
	if err != nil {
		return MyTime{}, err
	}
	if t.Before(f) {
		return MyTime{}, invalid("to must not be before from")
	}
	if t.Sub(f) > MaxMineDays*24*time.Hour {
		return MyTime{}, invalid("the period spans at most %d days", MaxMineDays)
	}
	st, err := s.store.GetSettings(ctx)
	if err != nil {
		return MyTime{}, err
	}
	entries, err := s.store.ListMine(ctx, p.UserID, from, to, MaxMineEntries)
	if err != nil {
		return MyTime{}, err
	}
	out := MyTime{Entries: make([]EntryView, 0, len(entries)), EditWindowDays: st.EditWindowDays}
	if len(entries) == 0 {
		return out, nil
	}
	seen := map[string]bool{}
	var ids []string
	for _, e := range entries {
		if !seen[e.SubjectID] {
			seen[e.SubjectID] = true
			ids = append(ids, e.SubjectID)
		}
	}
	access, err := s.tickets.Access(ctx, p.UserID, ids)
	if err != nil {
		return MyTime{}, err
	}
	for _, e := range entries {
		v := EntryView{Entry: e, Own: true, Editable: s.editable(p, e, st)}
		if a := access[e.SubjectID]; a.Found && a.CanView {
			v.Reference = a.Reference
		}
		out.TotalMinutes += e.DurationMinutes
		out.Entries = append(out.Entries, v)
	}
	return out, nil
}

// ---- Retention -------------------------------------------------------------------------------------------

// Purge hard-deletes entries whose work date is older than the retention, soft-deleted entries 30 days after their
// deletion and timers older than 30 days. It runs whether or not the module is switched on. One audit summary with
// counts is written when something was removed.
func (s *Service) Purge(ctx context.Context, correlationID string) (PurgeCounts, error) {
	st, err := s.store.GetSettings(ctx)
	if err != nil {
		return PurgeCounts{}, err
	}
	now := s.now()
	cutoff := s.today().AddDate(0, -st.RetentionMonths, 0).Format(time.DateOnly)
	var counts PurgeCounts
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		if counts, err = s.store.PurgeTx(ctx, tx, cutoff, now.Add(-SoftDeleteKeep), now.Add(-StaleTimerKeep)); err != nil {
			return err
		}
		if counts.Entries+counts.Deleted+counts.Timers == 0 {
			return nil
		}
		id, err := s.store.NewID(ctx, tx)
		if err != nil {
			return err
		}
		return recordAudit(ctx, tx, Caller{Actor: systemActor, CorrelationID: correlationID}, "purge.completed", "timetracking_purge", id, nil, nil,
			map[string]any{"entries": counts.Entries, "deletedEntries": counts.Deleted, "timers": counts.Timers, "workDateBefore": cutoff})
	})
	return counts, err
}

// HandlePurge is the job handler of timetracking.purge (daily, idempotent).
func (s *Service) HandlePurge(ctx context.Context, job jobs.Job) error {
	_, err := s.Purge(ctx, "job:"+job.ID)
	return err
}
