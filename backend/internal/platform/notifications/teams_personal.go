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

// Personal Microsoft Teams cards (F15 slice T-B, ADR-0036). A personal card is a Notification Delivery of channel
// "teams", created in the same transaction as the in-app notification for the categories that carry a TeamsCard,
// while the Teams module is on, the recipient did not opt out and has a linked Entra identity. The card is
// reference-only: generic wording, the reference number the recipient may know and a link back to Turaco.

// ChannelTeamsPersonal is the delivery channel of personal Teams cards (and the preference channel).
const ChannelTeamsPersonal = "teams"

// TeamsPersonalJobType is the job that sends one personal card (payload: deliveryId). Its type starts with the module
// key "teams", so the module gate keeps it pending while the module is switched off.
const TeamsPersonalJobType = "teams.personal_send"

// TeamsPersonalJobTimeout bounds one send including database bookkeeping.
const TeamsPersonalJobTimeout = 2 * time.Minute

const teamsPersonalMaxAttempts = 5

// PersonalCard is one personal Teams card as the port sees it. There is no field for record content.
type PersonalCard struct {
	Kind      string
	Category  string
	Reference string
	Headline  string
	Action    string
	LinkURL   string
	Locale    string
}

// PersonalPoster is the port of personal Teams cards; an adapter in integrations implements it behind a small wiring
// adapter, so platform code never imports an integration. Errors are *TransientChannelError, *PermanentChannelError
// or ErrRecipientUnreachable.
type PersonalPoster interface {
	SendPersonal(ctx context.Context, tenantID, objectID string, card PersonalCard) error
}

// ErrRecipientUnreachable means the Teams app is not installed for the recipient or Teams does not know them. It is
// permanent for this delivery.
var ErrRecipientUnreachable = errors.New("notifications: teams recipient unreachable")

// TeamsIdentityResolver answers the Teams identity of a User; Organization implements it over the Entra identity link.
type TeamsIdentityResolver interface {
	// TeamsIdentity returns the Entra tenant and object id of the User and whether the User is an active account that
	// may receive cards. A User without a link has linked false.
	TeamsIdentity(ctx context.Context, userID string) (tenantID, objectID string, linked bool, err error)
}

// TeamsPersonalOptions configure the personal Teams side of a Service.
type TeamsPersonalOptions struct {
	// Enabled reports whether the Teams module is on; nil means off. Cards are created only while it is on.
	Enabled func(ctx context.Context) (bool, error)
	// Identity answers whether a recipient has a Teams identity; nil means nobody has.
	Identity TeamsIdentityResolver
}

// WithTeamsPersonal returns a Service that creates personal Teams card deliveries (turaco-worker).
func (s *Service) WithTeamsPersonal(o TeamsPersonalOptions) *Service {
	cp := *s
	cp.teamsPersonal = &o
	return &cp
}

// scheduleTeamsPersonal creates the personal card delivery and its job, unless there is no reason to.
func (s *Service) scheduleTeamsPersonal(ctx context.Context, tx pgx.Tx, notificationID, userID, category string) error {
	o := s.teamsPersonal
	if o == nil || o.Enabled == nil || o.Identity == nil {
		return nil
	}
	cat, ok := s.categories.Lookup(category)
	if !ok || cat.TeamsCard == nil {
		return nil
	}
	if on, err := o.Enabled(ctx); err != nil {
		return fmt.Errorf("check teams module: %w", err)
	} else if !on {
		return nil
	}
	if _, _, linked, err := o.Identity.TeamsIdentity(ctx, userID); err != nil {
		return fmt.Errorf("resolve teams identity: %w", err)
	} else if !linked {
		return nil
	}
	optedOut, err := teamsOptedOut(ctx, tx, userID, category)
	if err != nil {
		return err
	}
	if optedOut {
		return nil
	}
	var deliveryID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO platform.notification_deliveries(notification_id, channel)
		VALUES ($1::uuid, $2) RETURNING id::text`, notificationID, ChannelTeamsPersonal).Scan(&deliveryID); err != nil {
		return fmt.Errorf("create teams delivery: %w", err)
	}
	if _, _, err := jobs.Enqueue(ctx, tx, jobs.EnqueueRequest{
		Type: TeamsPersonalJobType, Payload: map[string]string{"deliveryId": deliveryID},
		DedupeKey: "notification-teams:" + deliveryID, MaxAttempts: teamsPersonalMaxAttempts,
	}); err != nil {
		return fmt.Errorf("enqueue teams job: %w", err)
	}
	return nil
}

type queryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// teamsOptedOut reports an explicit opt-out; with no stored choice the personal card is on for the categories that
// carry one (the three T-B categories), as the design decides.
func teamsOptedOut(ctx context.Context, q queryRower, userID, category string) (bool, error) {
	var optedOut bool
	err := q.QueryRow(ctx, `
		SELECT NOT enabled FROM platform.notification_preferences
		WHERE user_id = $1::uuid AND category = $2 AND channel = $3`, userID, category, ChannelTeamsPersonal).Scan(&optedOut)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("read teams preference: %w", err)
	}
	return optedOut, nil
}

// TeamsPersonalSender sends the personal card jobs.
type TeamsPersonalSender struct {
	pool     *pgxpool.Pool
	poster   PersonalPoster
	identity TeamsIdentityResolver
	cats     *Registry
	baseURL  string
	locale   string
}

// NewTeamsPersonalSender creates the handler of TeamsPersonalJobType. baseURL is the web application's address used
// for links; locale is "en" or "de".
func NewTeamsPersonalSender(pool *pgxpool.Pool, poster PersonalPoster, identity TeamsIdentityResolver, categories *Registry, baseURL, locale string) *TeamsPersonalSender {
	return &TeamsPersonalSender{pool: pool, poster: poster, identity: identity, cats: categories, baseURL: strings.TrimRight(baseURL, "/"), locale: locale}
}

// Handle sends one delivery. It is idempotent: a delivery that is already delivered, failed or cancelled is left
// alone; a crash after Teams accepted the card but before the status update sends it again on retry (at-least-once).
// Stored errors are machine codes, never provider text.
func (t *TeamsPersonalSender) Handle(ctx context.Context, job jobs.Job) error {
	var p struct {
		DeliveryID string `json:"deliveryId"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil || !validUUID(p.DeliveryID) {
		return jobs.Permanent(errors.New("teams personal job: invalid payload"))
	}
	d, n, userID, err := claimDelivery(ctx, t.pool, p.DeliveryID, teamsPersonalMaxAttempts)
	if errors.Is(err, errNothingToDo) {
		return nil
	}
	if err != nil {
		return err
	}
	fail := func(code string, cause error, permanent bool) error {
		return releaseDelivery(ctx, t.pool, d, fmt.Errorf("%s: %w", code, cause), permanent, teamsPersonalMaxAttempts)
	}
	cat, ok := t.cats.Lookup(n.Category)
	if !ok || cat.TeamsCard == nil {
		return fail("category_unknown", errors.New("category has no Teams card"), true)
	}
	if optedOut, err := teamsOptedOut(ctx, t.pool, userID, n.Category); err != nil {
		return fail("database", err, false)
	} else if optedOut {
		return finishDelivery(ctx, t.pool, d, "cancelled", "recipient opted out")
	}
	tenant, object, linked, err := t.identity.TeamsIdentity(ctx, userID)
	if err != nil {
		return fail("identity_lookup", err, false)
	}
	if !linked {
		return finishDelivery(ctx, t.pool, d, "cancelled", "no_teams_identity")
	}
	reference := cardReference(n.Params)
	if reference == "" {
		return finishDelivery(ctx, t.pool, d, "cancelled", "no_reference")
	}
	path := ""
	if n.LinkType != nil && n.LinkID != nil {
		path = t.cats.linkPath(*n.LinkType, *n.LinkID)
	}
	if t.baseURL == "" || path == "" {
		return fail("base_url_missing", errors.New("no link can be built"), true)
	}
	locale := t.locale
	if locale != "de" {
		locale = "en"
	}
	text, ok := cat.TeamsCard.Texts[locale]
	if !ok {
		text = cat.TeamsCard.Texts["en"]
	}
	card := PersonalCard{Kind: cat.TeamsCard.Kind, Category: n.Category, Reference: reference, Headline: text.Headline,
		Action: text.Action, LinkURL: t.baseURL + path, Locale: locale}
	err = t.poster.SendPersonal(ctx, tenant, object, card)
	if err == nil {
		return finishDelivery(ctx, t.pool, d, "delivered", "")
	}
	var transient *TransientChannelError
	var permanent *PermanentChannelError
	switch {
	case errors.As(err, &transient):
		out := fail(transient.Code, err, false)
		if transient.RetryAfter > 0 && !jobs.IsPermanent(out) && ctx.Err() == nil {
			return jobs.RetryAfter(out, transient.RetryAfter)
		}
		return out
	case errors.Is(err, ErrRecipientUnreachable):
		return finishDelivery(ctx, t.pool, d, "cancelled", "recipient_unreachable")
	case errors.As(err, &permanent):
		return fail(permanent.Code, err, true)
	default:
		return fail("error", err, false)
	}
}

// cardReference extracts the reference number from a notification's title parameter ("TKT-000123 · Printer jams"
// carries the reference first). Only a token that looks like a reference number is used, never free text.
func cardReference(params map[string]any) string {
	title, _ := params["title"].(string)
	title = strings.TrimSpace(title)
	if i := strings.Index(title, " · "); i >= 0 {
		title = strings.TrimSpace(title[:i])
	}
	if !referencePattern.MatchString(title) {
		return ""
	}
	return title
}
