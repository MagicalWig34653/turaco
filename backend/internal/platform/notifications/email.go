package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/smtp"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

// EmailJobType is the job that sends one email delivery (payload: deliveryId).
const EmailJobType = "notifications.email.send"

// EmailJobTimeout bounds one send including database bookkeeping.
const EmailJobTimeout = 2 * time.Minute

// emailMaxAttempts is the job attempt limit; a delivery that exhausts it is failed.
const emailMaxAttempts = 5

// scheduleEmail creates the email delivery of a new notification and its job
// in the caller's transaction, unless the recipient opted out of the category.
func scheduleEmail(ctx context.Context, tx pgx.Tx, notificationID, userID, category string) error {
	var optedOut bool
	err := tx.QueryRow(ctx, `
		SELECT NOT enabled FROM platform.notification_preferences
		WHERE user_id = $1::uuid AND category = $2 AND channel = $3`, userID, category, ChannelEmail).Scan(&optedOut)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("read notification preference: %w", err)
	}
	if optedOut {
		return nil
	}
	var deliveryID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO platform.notification_deliveries(notification_id, channel)
		VALUES ($1::uuid, $2) RETURNING id::text`, notificationID, ChannelEmail).Scan(&deliveryID); err != nil {
		return fmt.Errorf("create email delivery: %w", err)
	}
	if _, _, err := jobs.Enqueue(ctx, tx, jobs.EnqueueRequest{
		Type: EmailJobType, Payload: map[string]string{"deliveryId": deliveryID},
		DedupeKey: "notification-email:" + deliveryID, MaxAttempts: emailMaxAttempts,
	}); err != nil {
		return fmt.Errorf("enqueue email job: %w", err)
	}
	return nil
}

// ContactResolver answers who can receive email; Organization implements it.
type ContactResolver interface {
	// EmailContact returns the User's email address and whether they may receive email
	// (active User with an address). Unknown Users are not deliverable.
	EmailContact(ctx context.Context, userID string) (email, displayName string, deliverable bool, err error)
}

// EmailSender sends the email channel jobs.
type EmailSender struct {
	pool     *pgxpool.Pool
	mailer   smtp.Mailer
	contacts ContactResolver
	baseURL  string
	locale   string
}

// NewEmailSender creates the handler of EmailJobType. baseURL is the web
// application's address used for links; locale is "en" or "de".
func NewEmailSender(pool *pgxpool.Pool, mailer smtp.Mailer, contacts ContactResolver, baseURL, locale string) *EmailSender {
	return &EmailSender{pool: pool, mailer: mailer, contacts: contacts, baseURL: baseURL, locale: locale}
}

// Handle sends one delivery. It is idempotent: a delivery that is already
// delivered, failed or cancelled is left alone. A crash after the relay
// accepted the mail but before the status update sends the mail again on
// retry (at-least-once, ADR-0024).
func (e *EmailSender) Handle(ctx context.Context, job jobs.Job) error {
	var payload struct {
		DeliveryID string `json:"deliveryId"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil || !validUUID(payload.DeliveryID) {
		return jobs.Permanent(errors.New("email job: invalid payload"))
	}

	d, n, userID, err := e.claim(ctx, payload.DeliveryID)
	if errors.Is(err, errNothingToDo) {
		return nil
	}
	if err != nil {
		return err
	}

	email, name, deliverable, err := e.contacts.EmailContact(ctx, userID)
	if err != nil {
		return e.release(ctx, d, fmt.Errorf("resolve recipient: %w", err), false)
	}
	if !deliverable {
		return e.finish(ctx, d, "cancelled", "recipient cannot receive email")
	}
	if enabled, err := e.emailEnabled(ctx, userID, n.Category); err != nil {
		return e.release(ctx, d, err, false)
	} else if !enabled {
		return e.finish(ctx, d, "cancelled", "recipient opted out")
	}
	rendered, err := renderEmail(e.locale, e.baseURL, n)
	if err != nil {
		return e.release(ctx, d, err, true)
	}
	to := email
	if name != "" {
		to = (&mailAddress{Name: name, Address: email}).String()
	}
	if err := e.mailer.Send(ctx, smtp.Message{To: to, Subject: rendered.Subject, Text: rendered.Text, HTML: rendered.HTML}); err != nil {
		return e.release(ctx, d, err, smtp.IsPermanent(err))
	}
	return e.finish(ctx, d, "delivered", "")
}

var errNothingToDo = errors.New("notifications: delivery is not open")

type delivery struct {
	id       string
	attempts int
}

// claim moves an open delivery to sending and returns it with its notification.
func (e *EmailSender) claim(ctx context.Context, id string) (delivery, Notification, string, error) {
	var (
		d      delivery
		n      Notification
		userID string
		raw    []byte
	)
	err := e.pool.QueryRow(ctx, `
		WITH claimed AS (
			UPDATE platform.notification_deliveries
			SET status = 'sending', attempts = attempts + 1, updated_at = now()
			WHERE id = $1::uuid AND status IN ('pending', 'sending')
			RETURNING id, notification_id, attempts
		)
		SELECT c.id::text, c.attempts, n.id::text, n.category, n.params, n.link_type, n.link_id::text,
		       n.created_at, n.read_at, n.recipient_user_id::text
		FROM claimed c JOIN platform.notifications n ON n.id = c.notification_id`, id).Scan(
		&d.id, &d.attempts, &n.ID, &n.Category, &raw, &n.LinkType, &n.LinkID, &n.CreatedAt, &n.ReadAt, &userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return delivery{}, Notification{}, "", errNothingToDo
	}
	if err != nil {
		return delivery{}, Notification{}, "", fmt.Errorf("claim email delivery: %w", err)
	}
	if err := json.Unmarshal(raw, &n.Params); err != nil {
		cause := fmt.Errorf("email delivery params: %w", err)
		_ = e.release(ctx, delivery{id: d.id, attempts: d.attempts}, cause, true)
		return delivery{}, Notification{}, "", jobs.Permanent(cause)
	}
	return d, n, userID, nil
}

func (e *EmailSender) emailEnabled(ctx context.Context, userID, category string) (bool, error) {
	var optedOut bool
	err := e.pool.QueryRow(ctx, `
		SELECT NOT enabled FROM platform.notification_preferences
		WHERE user_id = $1::uuid AND category = $2 AND channel = $3`, userID, category, ChannelEmail).Scan(&optedOut)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("read notification preference: %w", err)
	}
	return !optedOut, nil
}

// finish stores a terminal status. It uses a fresh context so a job that is
// being shut down still records what the relay accepted.
func (e *EmailSender) finish(ctx context.Context, d delivery, status, reason string) error {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var lastError *string
	if reason != "" {
		lastError = &reason
	}
	_, err := e.pool.Exec(fctx, `
		UPDATE platform.notification_deliveries
		SET status = $2, last_error = $3, updated_at = now(),
		    delivered_at = CASE WHEN $2 = 'delivered' THEN now() ELSE NULL END
		WHERE id = $1::uuid`, d.id, status, lastError)
	if err != nil {
		return fmt.Errorf("finish email delivery: %w", err)
	}
	return nil
}

// release records a failed attempt. A permanent failure (or the last allowed
// attempt) ends the delivery as failed; otherwise it returns to pending and
// the job is retried by the runner with back-off.
func (e *EmailSender) release(ctx context.Context, d delivery, cause error, permanent bool) error {
	// An attempt interrupted by shutdown is not a failure: the runner refunds
	// the job attempt, so the delivery attempt is refunded too and the
	// delivery stays open for the next worker.
	if ctx.Err() != nil && !permanent {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, _ = e.pool.Exec(fctx, `
			UPDATE platform.notification_deliveries
			SET status = 'pending', attempts = greatest(attempts - 1, 0), updated_at = now()
			WHERE id = $1::uuid AND status = 'sending'`, d.id)
		return ctx.Err()
	}
	terminal := permanent || d.attempts >= emailMaxAttempts
	status := "pending"
	if terminal {
		status = "failed"
	}
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	msg := truncate(cause.Error(), 1000)
	if _, err := e.pool.Exec(fctx, `
		UPDATE platform.notification_deliveries SET status = $2, last_error = $3, updated_at = now()
		WHERE id = $1::uuid`, d.id, status, msg); err != nil {
		return fmt.Errorf("record email failure: %w", err)
	}
	if terminal {
		return jobs.Permanent(cause)
	}
	return cause
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// mailAddress formats "Name <address>" with RFC 5322 quoting.
type mailAddress struct{ Name, Address string }

func (a *mailAddress) String() string {
	return (&mail.Address{Name: a.Name, Address: a.Address}).String()
}
