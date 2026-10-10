package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

// ChannelCard is one channel post as the port sees it: generic wording, a reference number and a link. There is no
// field for record content.
type ChannelCard struct {
	Kind      string
	Category  string
	Reference string
	Headline  string
	Action    string
	LinkURL   string
	Locale    string
}

// ChannelPoster is the port of the channel (Teams) side. An adapter in integrations implements it behind a small
// wiring adapter, so platform code never imports an integration.
type ChannelPoster interface {
	PostToChannel(ctx context.Context, destinationKey string, card ChannelCard) error
}

// TransientChannelError marks a failure worth retrying (network, 5xx, rate limiting); RetryAfter is the provider's
// Retry-After when it sent one. Code is a short machine code that is stored and shown.
type TransientChannelError struct {
	Code       string
	RetryAfter time.Duration
}

func (e *TransientChannelError) Error() string { return "channel post: transient failure: " + e.Code }

// PermanentChannelError marks a failure that retrying cannot fix. Code is a short machine code.
type PermanentChannelError struct{ Code string }

func (e *PermanentChannelError) Error() string { return "channel post: permanent failure: " + e.Code }

// ChannelSender sends the channel post jobs.
type ChannelSender struct {
	pool    *pgxpool.Pool
	poster  ChannelPoster
	cats    *Registry
	baseURL string
	locale  string
	now     func() time.Time
}

// NewChannelSender creates the handler of ChannelPostJobType. baseURL is the web application's address used for
// links; locale is "en" or "de".
func NewChannelSender(pool *pgxpool.Pool, poster ChannelPoster, categories *Registry, baseURL, locale string) *ChannelSender {
	return &ChannelSender{pool: pool, poster: poster, cats: categories, baseURL: strings.TrimRight(baseURL, "/"), locale: locale, now: time.Now}
}

type channelDelivery struct {
	id       string
	attempts int
	key      string
	payload  postPayload
	created  time.Time
}

// Handle sends one delivery. It is idempotent: a delivery that is already delivered, failed or cancelled is left
// alone. A crash after the provider accepted the post but before the status update posts again on retry
// (at-least-once, ADR-0024). Stored errors are machine codes, never provider text or URLs.
func (c *ChannelSender) Handle(ctx context.Context, job jobs.Job) error {
	var p struct {
		DeliveryID string `json:"deliveryId"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil || !validUUID(p.DeliveryID) {
		return jobs.Permanent(errors.New("channel post job: invalid payload"))
	}
	d, err := c.claim(ctx, p.DeliveryID)
	if errors.Is(err, errNothingToDo) {
		return nil
	}
	if err != nil {
		return err
	}
	if c.now().Sub(d.created) > ChannelPostMaxAge {
		return c.finish(ctx, d, "cancelled", "stale")
	}
	var routed bool
	if err := c.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM platform.notification_channel_routes
		               WHERE channel = $1 AND category = $2 AND destination_key = $3)`,
		ChannelTeams, d.payload.Category, d.key).Scan(&routed); err != nil {
		return c.release(ctx, d, fmt.Errorf("check channel route: %w", err), "database", false)
	}
	if !routed {
		return c.finish(ctx, d, "cancelled", "route_removed")
	}
	text, ok := c.cats.ChannelText(d.payload.Category, d.payload.Kind, c.locale)
	path := c.cats.linkPath(d.payload.LinkType, d.payload.LinkID)
	switch {
	case !ok:
		return c.release(ctx, d, errors.New("category cannot be posted"), "category_unknown", true)
	case c.baseURL == "" || path == "":
		return c.release(ctx, d, errors.New("no link can be built"), "base_url_missing", true)
	}
	locale := c.locale
	if locale != "de" {
		locale = "en"
	}
	card := ChannelCard{Kind: d.payload.Kind, Category: d.payload.Category, Reference: d.payload.Reference,
		Headline: text.Headline, Action: text.Action, LinkURL: c.baseURL + path, Locale: locale}
	err = c.poster.PostToChannel(ctx, d.key, card)
	if err == nil {
		return c.finish(ctx, d, "delivered", "")
	}
	var transient *TransientChannelError
	var permanent *PermanentChannelError
	switch {
	case errors.As(err, &transient):
		out := c.release(ctx, d, err, transient.Code, false)
		if transient.RetryAfter > 0 && !jobs.IsPermanent(out) && ctx.Err() == nil {
			return jobs.RetryAfter(out, transient.RetryAfter)
		}
		return out
	case errors.As(err, &permanent):
		return c.release(ctx, d, err, permanent.Code, true)
	default:
		// An adapter that reports neither class is treated as transient with an opaque code.
		return c.release(ctx, d, err, "error", false)
	}
}

func (c *ChannelSender) claim(ctx context.Context, id string) (channelDelivery, error) {
	var d channelDelivery
	var raw []byte
	err := c.pool.QueryRow(ctx, `
		UPDATE platform.notification_deliveries
		SET status = 'sending', attempts = attempts + 1, updated_at = now()
		WHERE id = $1::uuid AND channel = $2 AND status IN ('pending', 'sending')
		RETURNING id::text, attempts, destination_key, payload, created_at`, id, ChannelTeams).Scan(&d.id, &d.attempts, &d.key, &raw, &d.created)
	if errors.Is(err, pgx.ErrNoRows) {
		return channelDelivery{}, errNothingToDo
	}
	if err != nil {
		return channelDelivery{}, fmt.Errorf("claim channel delivery: %w", err)
	}
	if err := json.Unmarshal(raw, &d.payload); err != nil {
		cause := errors.New("channel delivery payload is invalid")
		_ = c.release(ctx, d, cause, "payload_invalid", true)
		return channelDelivery{}, jobs.Permanent(cause)
	}
	return d, nil
}

// finish stores a terminal status with a fresh context so a job being shut down still records the outcome.
func (c *ChannelSender) finish(ctx context.Context, d channelDelivery, status, code string) error {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var lastError *string
	if code != "" {
		lastError = &code
	}
	if _, err := c.pool.Exec(fctx, `
		UPDATE platform.notification_deliveries
		SET status = $2, last_error = $3, updated_at = now(),
		    delivered_at = CASE WHEN $2 = 'delivered' THEN now() ELSE NULL END
		WHERE id = $1::uuid`, d.id, status, lastError); err != nil {
		return fmt.Errorf("finish channel delivery: %w", err)
	}
	return nil
}

// release records a failed attempt with its machine code. A permanent failure or the last allowed attempt ends the
// delivery as failed; otherwise it returns to pending and the job runner retries with back-off.
func (c *ChannelSender) release(ctx context.Context, d channelDelivery, cause error, code string, permanent bool) error {
	if ctx.Err() != nil && !permanent {
		// Interrupted by shutdown: not a failure, the attempt is refunded.
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, _ = c.pool.Exec(fctx, `
			UPDATE platform.notification_deliveries
			SET status = 'pending', attempts = greatest(attempts - 1, 0), updated_at = now()
			WHERE id = $1::uuid AND status = 'sending'`, d.id)
		return ctx.Err()
	}
	terminal := permanent || d.attempts >= channelMaxAttempts
	status := "pending"
	if terminal {
		status = "failed"
	}
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err := c.pool.Exec(fctx, `
		UPDATE platform.notification_deliveries SET status = $2, last_error = $3, updated_at = now()
		WHERE id = $1::uuid`, d.id, status, truncate(code, 60)); err != nil {
		return fmt.Errorf("record channel failure: %w", err)
	}
	if terminal {
		return jobs.Permanent(cause)
	}
	return cause
}
