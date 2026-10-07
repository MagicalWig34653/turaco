package application

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/remoteaccess"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// ImportResult counts what one import did.
type ImportResult struct {
	// Records were stored or refreshed; Skipped were invalid or conflicting and ignored.
	Records, Skipped int
	// Flagged records are unattributed, duplicate or after the session's close.
	Flagged int
	// SessionsUpdated got observed facts from their attributed record.
	SessionsUpdated int
}

var providerSessionID = regexp.MustCompile(`^[A-Za-z0-9_.:@-]{1,100}$`)

// validRecord checks a provider record before it is stored: bounded identifiers, an allow-listed source and sane
// times. A record that fails is skipped, never imported and never fatal.
func validRecord(provider string, o remoteaccess.ObservedSession, now time.Time) bool {
	limit := now.Add(clockSkew)
	switch {
	case !providerSessionID.MatchString(o.ProviderSessionID),
		!remoteaccess.ValidPeerID(provider, o.PeerID),
		!slices.Contains(ObservationSources, o.Source),
		o.StartedAt.IsZero() || o.StartedAt.After(limit) || o.StartedAt.Before(now.Add(-maxRecordAge)),
		o.ObservedAt.IsZero() || o.ObservedAt.After(limit),
		o.EndedAt != nil && (o.EndedAt.Before(o.StartedAt) || o.EndedAt.After(limit) || o.EndedAt.Sub(o.StartedAt) > maxRecordDuration):
		return false
	}
	if o.Operator != "" && (utf8.RuneCountInString(o.Operator) > maxOperator || !utf8.ValidString(o.Operator) || safetext.ContainsUnsafe(o.Operator, false)) {
		return false
	}
	return true
}

// ImportObservations stores the provider's connection records and attributes each to at most one session: the
// launched session of the same peer id whose window [launched_at, closed_at + slack] contains the record's start,
// when no other session qualifies (several candidates leave the record unattributed). A second record of an
// already attributed session is flagged duplicate, one that starts after the session was closed after_close, and
// a record without a session unattributed; flagged records never feed a session. A session gets its observed
// facts only from its attributed record. Nothing here creates a session, changes a status, infers an end or
// raises a finding; invalid records are skipped and counted in the result. Only newly flagged records are audited
// (one entry per run). Idempotent.
func (s *Service) ImportObservations(ctx context.Context, correlationID, provider string, observed []remoteaccess.ObservedSession) (ImportResult, error) {
	c := Caller{Actor: audit.SystemActor("remoteaccess-observer"), CorrelationID: correlationID}
	now := s.now()
	var res ImportResult
	var valid []remoteaccess.ObservedSession
	for _, o := range observed {
		if validRecord(provider, o, now) {
			valid = append(valid, o)
		} else {
			res.Skipped++
		}
	}
	// Earliest start first, so the first record of a session is its attributed one.
	slices.SortStableFunc(valid, func(a, b remoteaccess.ObservedSession) int { return a.StartedAt.Compare(b.StartedAt) })
	var first error
	for _, o := range valid {
		err := s.store.InTx(ctx, func(tx pgx.Tx) error { return s.importRecord(ctx, tx, c, provider, o, now, &res) })
		if errors.Is(err, ErrRecordConflict) {
			res.Skipped++
		} else if err != nil && first == nil {
			first = err
		}
	}
	if first == nil && res.Flagged > 0 {
		first = s.store.InTx(ctx, func(tx pgx.Tx) error {
			return recordAudit(ctx, tx, c, "observation.import_reviewed", "remote_access_provider", provider, nil, nil,
				map[string]any{"provider": provider, "records": res.Records, "skipped": res.Skipped, "flagged": res.Flagged})
		})
	}
	return res, first
}

func (s *Service) importRecord(ctx context.Context, tx pgx.Tx, c Caller, provider string, o remoteaccess.ObservedSession, now time.Time, res *ImportResult) error {
	rec := ProviderRecord{Provider: provider, ProviderSessionID: o.ProviderSessionID, PeerID: o.PeerID, StartedAt: o.StartedAt.UTC(),
		EndedAt: utcPtr(o.EndedAt), Operator: strPtr(o.Operator), Source: o.Source, ObservedAt: o.ObservedAt.UTC()}
	rec, err := s.store.UpsertRecordTx(ctx, tx, rec)
	if err != nil {
		return err
	}
	res.Records++
	changed := false
	if rec.SessionID == nil {
		before := rec.Flag
		if rec, err = s.attribute(ctx, tx, rec); err != nil {
			return err
		}
		changed = (before == nil) != (rec.Flag == nil) || (before != nil && rec.Flag != nil && *before != *rec.Flag) || rec.SessionID != nil
	}
	if rec.Flag != nil && (rec.New || changed) {
		res.Flagged++
	}
	if rec.SessionID == nil || rec.Flag != nil {
		return nil
	}
	cur, err := s.store.LockSessionTx(ctx, tx, *rec.SessionID)
	if err != nil {
		return err
	}
	if sameObservation(cur, rec) {
		return nil
	}
	next := cur
	conn, at, src := rec.StartedAt, rec.ObservedAt, rec.Source
	next.ObservedConnectedAt, next.ObservedEndedAt, next.ObservedSource, next.ObservedAt = &conn, rec.EndedAt, &src, &at
	if _, err := s.commit(ctx, tx, c, cur, next, "observation_imported", "", map[string]any{"source": src, "ended": rec.EndedAt != nil}); err != nil {
		return err
	}
	res.SessionsUpdated++
	return nil
}

// attribute decides the session of a record that has none yet (see ImportObservations) and stores the outcome.
func (s *Service) attribute(ctx context.Context, tx pgx.Tx, rec ProviderRecord) (ProviderRecord, error) {
	cands, err := s.store.CandidateSessions(ctx, rec.Provider, rec.PeerID, rec.StartedAt)
	if err != nil {
		return rec, err
	}
	if len(cands) != 1 {
		return s.setAttribution(ctx, tx, rec, nil, FlagUnattributed)
	}
	sess, err := s.store.LockSessionTx(ctx, tx, cands[0].ID)
	if err != nil {
		return rec, err
	}
	switch {
	case sess.ClosedAt != nil && rec.StartedAt.After(*sess.ClosedAt):
		return s.setAttribution(ctx, tx, rec, &sess.ID, FlagAfterClose)
	default:
		taken, err := s.store.HasAttributedRecordTx(ctx, tx, sess.ID, rec.ID)
		if err != nil {
			return rec, err
		}
		if taken {
			return s.setAttribution(ctx, tx, rec, &sess.ID, FlagDuplicate)
		}
		return s.setAttribution(ctx, tx, rec, &sess.ID, "")
	}
}

func (s *Service) setAttribution(ctx context.Context, tx pgx.Tx, rec ProviderRecord, sessionID *string, flag string) (ProviderRecord, error) {
	f := strPtr(flag)
	if sessionID == nil && rec.SessionID == nil && rec.Flag != nil && f != nil && *rec.Flag == *f {
		return rec, nil
	}
	if err := s.store.SetRecordAttributionTx(ctx, tx, rec.ID, sessionID, f); err != nil {
		return rec, err
	}
	rec.SessionID, rec.Flag = sessionID, f
	return rec, nil
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func sameObservation(cur Session, r ProviderRecord) bool {
	eq := func(a, b *time.Time) bool {
		if a == nil || b == nil {
			return a == nil && b == nil
		}
		return a.Equal(*b)
	}
	return cur.ObservedSource != nil && *cur.ObservedSource == r.Source && cur.ObservedConnectedAt != nil &&
		cur.ObservedConnectedAt.Equal(r.StartedAt) && eq(cur.ObservedEndedAt, r.EndedAt)
}

// HandleExpire is the job handler of remoteaccess.expire_sessions.
func (s *Service) HandleExpire(ctx context.Context, job jobs.Job) error {
	_, err := s.ExpireSessions(ctx, "job:"+job.ID)
	return err
}

// HandleObserve is the job handler of remoteaccess.observe: every enabled provider that implements
// SessionObserver is read and its records are stored and attributed. A failing provider does not stop the others; the first
// error is returned afterwards so the job retries.
func (s *Service) HandleObserve(ctx context.Context, job jobs.Job) error {
	var first error
	since := s.now().Add(-ObservationLookback)
	for _, key := range s.providers.Keys() {
		prov, _ := s.providers.Get(key)
		obs, ok := prov.(remoteaccess.SessionObserver)
		if !ok {
			continue
		}
		list, err := obs.Sessions(ctx, since)
		if err == nil {
			_, err = s.ImportObservations(ctx, "job:"+job.ID, key, list)
		}
		if err != nil && !errors.Is(err, remoteaccess.ErrUnsupported) && first == nil {
			first = err
		}
	}
	return first
}
