package application

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/remoteaccess"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

// matchObserved finds the provider record that belongs to a Turaco session: same peer id and a start inside the
// session's time window (launch minus slack until close, or now, plus slack). Nothing else is used. The earliest
// start wins.
func matchObserved(sess Session, observed []remoteaccess.ObservedSession, now time.Time) (remoteaccess.ObservedSession, bool) {
	if sess.LaunchedAt == nil {
		return remoteaccess.ObservedSession{}, false
	}
	from := sess.LaunchedAt.Add(-ObservationSlack)
	to := now
	if sess.ClosedAt != nil {
		to = *sess.ClosedAt
	}
	to = to.Add(ObservationSlack)
	var best remoteaccess.ObservedSession
	found := false
	for _, o := range observed {
		if o.PeerID != sess.PeerID || o.StartedAt.Before(from) || o.StartedAt.After(to) {
			continue
		}
		if o.EndedAt != nil && o.EndedAt.Before(o.StartedAt) {
			continue
		}
		if !found || o.StartedAt.Before(best.StartedAt) {
			best, found = o, true
		}
	}
	return best, found
}

// ImportObservations stores provider records on the launched Turaco sessions they match (peer id and time window
// only). It never creates sessions, never changes a status and never infers an end: a mismatch raises nothing in
// R-A (docs/product/f10-remote-access-design.md). It returns the number of sessions updated. Idempotent.
func (s *Service) ImportObservations(ctx context.Context, correlationID, provider string, observed []remoteaccess.ObservedSession) (int, error) {
	c := Caller{Actor: audit.SystemActor("remoteaccess-observer"), CorrelationID: correlationID}
	now := s.now()
	sessions, err := s.store.ObservableSessions(ctx, provider, now.Add(-ObservationLookback))
	if err != nil {
		return 0, err
	}
	updated := 0
	for _, sess := range sessions {
		o, ok := matchObserved(sess, observed, now)
		if !ok || o.Source == "" {
			continue
		}
		err := s.store.InTx(ctx, func(tx pgx.Tx) error {
			cur, err := s.store.LockSessionTx(ctx, tx, sess.ID)
			if err != nil {
				return err
			}
			if sameObservation(cur, o) {
				return nil
			}
			next := cur
			conn := o.StartedAt.UTC()
			at := o.ObservedAt.UTC()
			src := o.Source
			next.ObservedConnectedAt, next.ObservedEndedAt, next.ObservedSource, next.ObservedAt = &conn, utcPtr(o.EndedAt), &src, &at
			if _, err := s.commit(ctx, tx, c, cur, next, "observation_imported", "", map[string]any{"source": src, "ended": o.EndedAt != nil}); err != nil {
				return err
			}
			updated++
			return nil
		})
		if err != nil {
			return updated, err
		}
	}
	return updated, nil
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func sameObservation(cur Session, o remoteaccess.ObservedSession) bool {
	eq := func(a, b *time.Time) bool {
		if a == nil || b == nil {
			return a == nil && b == nil
		}
		return a.Equal(*b)
	}
	return cur.ObservedSource != nil && *cur.ObservedSource == o.Source && cur.ObservedConnectedAt != nil &&
		cur.ObservedConnectedAt.Equal(o.StartedAt) && eq(cur.ObservedEndedAt, o.EndedAt)
}

// HandleExpire is the job handler of remoteaccess.expire_sessions.
func (s *Service) HandleExpire(ctx context.Context, job jobs.Job) error {
	_, err := s.ExpireSessions(ctx, "job:"+job.ID)
	return err
}

// HandleObserve is the job handler of remoteaccess.observe: every enabled provider that implements
// SessionObserver is read and its records are matched. A failing provider does not stop the others; the first
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
