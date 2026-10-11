package repository

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

// Access expiry of external accounts (ADR-0034 point 5). The request path already treats an account past its end of
// access as inactive (UserAccess, LockActiveUser); this job writes the status, revokes sessions and credential
// tokens, audits and publishes AccessExpired, and reminds the sponsor.

var _ application.ExpiryStore = (*Repository)(nil)

// Notifier creates the reminder notifications inside the job's transaction (platform/notifications.Service).
type Notifier interface {
	Create(ctx context.Context, tx pgx.Tx, in notifications.Intent) (bool, error)
}

// WithNotifier returns a copy that can remind sponsors (turaco-worker).
func (r *Repository) WithNotifier(n Notifier) *Repository {
	c := *r
	c.notifier = n
	return &c
}

// expiryActor is the actor of the expiry job in the audit trail ("system:access-expiry").
func expiryActor() audit.Actor { return audit.SystemActor("access-expiry") }

// ExpireAccess implements application.ExpiryStore. Rows are claimed FOR UPDATE SKIP LOCKED, so concurrent workers
// split the backlog; a repeated run finds nothing.
func (r *Repository) ExpireAccess(ctx context.Context, now time.Time, limit int) (int, error) {
	if r.sessions == nil {
		return 0, application.ErrNoGuards
	}
	n := 0
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, external_party_id::text, access_expires_at FROM organization.users
			WHERE account_kind = 'external' AND status = 'active' AND access_expires_at <= $1
			ORDER BY access_expires_at, id LIMIT $2 FOR UPDATE SKIP LOCKED`, now, limit)
		if err != nil {
			return fmt.Errorf("find expired external accounts: %w", err)
		}
		type due struct {
			id, party string
			ended     time.Time
		}
		var list []due
		for rows.Next() {
			var d due
			if err := rows.Scan(&d.id, &d.party, &d.ended); err != nil {
				rows.Close()
				return fmt.Errorf("find expired external accounts: scan: %w", err)
			}
			list = append(list, d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("find expired external accounts: %w", err)
		}
		actor := expiryActor()
		corr := "access-expiry-" + now.UTC().Format("20060102T150405")
		for _, d := range list {
			if _, err := tx.Exec(ctx, `
				UPDATE organization.users SET status = 'inactive', status_source = 'platform', version = version + 1, updated_at = $2
				WHERE id = $1::uuid AND status = 'active'`, d.id, now); err != nil {
				return fmt.Errorf("expire external account: %w", err)
			}
			sessions, err := r.sessions.RevokeSessions(ctx, tx, d.id, "access_expired", actor, corr, now)
			if err != nil {
				return err
			}
			tokens, err := r.sessions.RevokeCredentialTokens(ctx, tx, d.id, "access_expired", actor, corr, now)
			if err != nil {
				return err
			}
			if err := audit.Record(ctx, tx, audit.Change{
				Action: "organization.user.access_expired", TargetType: "user", TargetID: d.id, Actor: actor, CorrelationID: corr,
				Before: map[string]any{"status": application.StatusActive}, After: map[string]any{"status": application.StatusInactive, "statusSource": "platform"},
				Metadata: map[string]any{"accessExpiresAt": d.ended.UTC().Format(time.RFC3339), "externalPartyId": d.party,
					"sessionsRevoked": sessions, "credentialTokensRevoked": tokens},
			}); err != nil {
				return err
			}
			if err := events.Publish(ctx, tx, events.Publication{Type: "AccessExpired", CorrelationID: corr,
				Payload: map[string]any{"userId": d.id, "externalPartyId": d.party}}); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// RemindSponsors implements application.ExpiryStore.
func (r *Repository) RemindSponsors(ctx context.Context, now time.Time, leadDays []int, limit int) (int, error) {
	if r.notifier == nil {
		return 0, errors.New("organization: sponsor reminders need a notifier")
	}
	created := 0
	leads := append([]int(nil), leadDays...)
	sort.Sort(sort.Reverse(sort.IntSlice(leads)))
	for i, lead := range leads {
		// Each lead time owns the window up to the next smaller one, so an account ending in two days is reminded
		// once ("3 days"), not once per lead time.
		from, horizon := now, now.AddDate(0, 0, lead)
		if i+1 < len(leads) {
			from = now.AddDate(0, 0, leads[i+1])
		}
		rows, err := r.pool.Query(ctx, `
			SELECT u.id::text, u.display_name, u.access_expires_at, p.sponsor_user_id::text
			FROM organization.users u JOIN organization.external_parties p ON p.id = u.external_party_id
			WHERE u.account_kind = 'external' AND u.status = 'active' AND u.access_expires_at > $1 AND u.access_expires_at <= $2
			ORDER BY u.access_expires_at, u.id LIMIT $3`, from, horizon, limit)
		if err != nil {
			return created, fmt.Errorf("find expiring external accounts: %w", err)
		}
		type due struct {
			id, name, sponsor string
			ends              time.Time
		}
		var list []due
		for rows.Next() {
			var d due
			if err := rows.Scan(&d.id, &d.name, &d.ends, &d.sponsor); err != nil {
				rows.Close()
				return created, fmt.Errorf("find expiring external accounts: scan: %w", err)
			}
			list = append(list, d)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return created, fmt.Errorf("find expiring external accounts: %w", err)
		}
		for _, d := range list {
			// One notification per sponsor, account, lead time and end date: an extension changes the end date and
			// so starts a fresh series; retries and parallel workers cannot send it twice.
			key := "access-expiring:" + d.id + ":" + strconv.Itoa(lead) + ":" + strconv.FormatInt(d.ends.Unix(), 10)
			err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
				ok, err := r.notifier.Create(ctx, tx, notifications.Intent{RecipientUserID: d.sponsor, Category: application.NotificationAccessExpiring,
					Params:   map[string]any{"title": d.name, "accessExpiresAt": d.ends.UTC().Format(time.RFC3339), "leadDays": lead},
					LinkType: application.LinkSponsoredAccount, LinkID: d.id, DedupeKey: key})
				if ok {
					created++
				}
				return err
			})
			if err != nil {
				return created, fmt.Errorf("remind sponsor: %w", err)
			}
		}
	}
	return created, nil
}
