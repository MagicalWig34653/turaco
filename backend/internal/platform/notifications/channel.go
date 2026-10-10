package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

// ChannelTeams is the delivery channel of Microsoft Teams channel posts (ADR-0036).
const ChannelTeams = "teams_channel"

// ChannelPostJobType is the job that sends one channel post (payload: deliveryId). Its type starts with the module
// key "teams", so the module gate keeps it pending while the module is switched off.
const ChannelPostJobType = "teams.channel_post"

// ChannelPostJobTimeout bounds one send including database bookkeeping.
const ChannelPostJobTimeout = 2 * time.Minute

// channelMaxAttempts is the job attempt limit of a channel post.
const channelMaxAttempts = 8

// ChannelPostMaxAge is how old a post may be when it is finally sent; an older one (the module was off, the
// destination was down) is cancelled as stale because a late incident post misleads more than it helps.
const ChannelPostMaxAge = 6 * time.Hour

var (
	// ErrForbidden means the caller may not manage channel routes.
	ErrForbidden = errors.New("notifications: not permitted")
	// ErrNotBroadcastable means the category is unknown or its owner did not mark it broadcastable.
	ErrNotBroadcastable = errors.New("notifications: category is not broadcastable")
	// ErrUnknownDestination means the destination key is not configured.
	ErrUnknownDestination = errors.New("notifications: unknown channel destination")
	// ErrRouteExists means the route already exists.
	ErrRouteExists = errors.New("notifications: channel route exists")
	// ErrRouteNotFound means no such route.
	ErrRouteNotFound = errors.New("notifications: channel route not found")
)

// referencePattern is the shape of a reference number in a channel post: no spaces, so a title cannot be passed.
var referencePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$`)

// ChannelOptions configure the channel (Teams) side of a Service.
type ChannelOptions struct {
	// Enabled reports whether the channel module is on; nil means off. Posts are created only while it is on.
	Enabled func(ctx context.Context) (bool, error)
	// DestinationKeys lists the configured destination keys (never URLs); nil means none.
	DestinationKeys func() []string
	// Mode is the adapter mode shown to administrators: real, fake or not_configured.
	Mode string
}

// WithChannelPosts returns a Service that creates channel post deliveries (turaco-worker) and validates routes
// against the configured destinations (turaco-api).
func (s *Service) WithChannelPosts(o ChannelOptions) *Service {
	cp := *s
	cp.channel = o
	return &cp
}

// ChannelInfo describes the channel for administrators.
type ChannelInfo struct {
	Mode         string
	Destinations []string
	Categories   []string
}

// ChannelInfo returns the adapter mode, the destination keys and the broadcastable categories.
func (s *Service) ChannelInfo() ChannelInfo {
	info := ChannelInfo{Mode: s.channel.Mode, Categories: s.categories.Broadcastable()}
	if s.channel.DestinationKeys != nil {
		info.Destinations = slices.Clone(s.channel.DestinationKeys())
	}
	if info.Destinations == nil {
		info.Destinations = []string{}
	}
	if info.Mode == "" {
		info.Mode = "not_configured"
	}
	return info
}

// ChannelPost is a request to post about a record to the routed channel destinations. It is reference-only: the
// producer passes the reference number a channel member may see and the record it links to, nothing else.
type ChannelPost struct {
	Category string
	// Kind is PostKindDeclared, PostKindUpdated or PostKindScheduled; the category must describe it.
	Kind      string
	Reference string
	LinkType  string
	LinkID    string
	// DedupeKey makes the post idempotent per destination (typically the outbox event id). Required.
	DedupeKey string
}

type postPayload struct {
	Category  string `json:"category"`
	Kind      string `json:"kind"`
	Reference string `json:"reference"`
	LinkType  string `json:"linkType"`
	LinkID    string `json:"linkId"`
}

// PostToChannels creates one delivery (and its job) per route of the category inside the caller's transaction and
// returns how many were created; a repeated DedupeKey is a no-op. With the channel module off, without a route or
// for a category without a route nothing is created.
func (s *Service) PostToChannels(ctx context.Context, tx pgx.Tx, in ChannelPost) (int, error) {
	if _, ok := s.categories.ChannelText(in.Category, in.Kind, "en"); !ok {
		return 0, fmt.Errorf("notifications: category %q cannot be posted as %q", in.Category, in.Kind)
	}
	switch {
	case !referencePattern.MatchString(in.Reference):
		return 0, errors.New("notifications: a channel post needs a reference number without free text")
	case in.DedupeKey == "":
		return 0, errors.New("notifications: dedupe key is required")
	case in.LinkType == "" || !validUUID(in.LinkID):
		return 0, errors.New("notifications: a channel post needs a link type and a link id")
	}
	if s.channel.Enabled == nil {
		return 0, nil
	}
	on, err := s.channel.Enabled(ctx)
	if err != nil {
		return 0, fmt.Errorf("check channel module: %w", err)
	}
	if !on {
		return 0, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT destination_key FROM platform.notification_channel_routes
		WHERE channel = $1 AND category = $2 ORDER BY destination_key`, ChannelTeams, in.Category)
	if err != nil {
		return 0, fmt.Errorf("list channel routes: %w", err)
	}
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return 0, fmt.Errorf("list channel routes: scan: %w", err)
		}
		keys = append(keys, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("list channel routes: %w", err)
	}
	payload, err := json.Marshal(postPayload{Category: in.Category, Kind: in.Kind, Reference: in.Reference, LinkType: in.LinkType, LinkID: in.LinkID})
	if err != nil {
		return 0, fmt.Errorf("marshal channel post: %w", err)
	}
	created := 0
	for _, key := range keys {
		var id string
		err := tx.QueryRow(ctx, `
			INSERT INTO platform.notification_deliveries(channel, destination_key, dedupe_key, payload)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (channel, destination_key, dedupe_key) WHERE dedupe_key IS NOT NULL DO NOTHING
			RETURNING id::text`, ChannelTeams, key, in.DedupeKey, payload).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return created, fmt.Errorf("create channel delivery: %w", err)
		}
		if _, _, err := jobs.Enqueue(ctx, tx, jobs.EnqueueRequest{
			Type: ChannelPostJobType, Payload: map[string]string{"deliveryId": id},
			DedupeKey: "channel-post:" + id, MaxAttempts: channelMaxAttempts,
		}); err != nil {
			return created, fmt.Errorf("enqueue channel post job: %w", err)
		}
		created++
	}
	return created, nil
}

// Route maps a broadcastable category to a channel destination key.
type Route struct {
	ID             string
	Category       string
	DestinationKey string
	CreatedBy      string
	CreatedAt      time.Time
}

// ListRoutes returns all Teams channel routes, ordered by category and destination.
func (s *Service) ListRoutes(ctx context.Context) ([]Route, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, category, destination_key, created_by::text, created_at
		FROM platform.notification_channel_routes WHERE channel = $1 ORDER BY category, destination_key`, ChannelTeams)
	if err != nil {
		return nil, fmt.Errorf("list channel routes: %w", err)
	}
	defer rows.Close()
	out := []Route{}
	for rows.Next() {
		var r Route
		if err := rows.Scan(&r.ID, &r.Category, &r.DestinationKey, &r.CreatedBy, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("list channel routes: scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CreateRoute adds a route and audits it in the same transaction. canManage is the caller's
// integrations.teams.manage; only broadcastable categories and configured destinations can be routed.
func (s *Service) CreateRoute(ctx context.Context, actorID string, canManage bool, category, destinationKey, correlationID string) (Route, error) {
	if actorID == "" || !canManage {
		return Route{}, ErrForbidden
	}
	if !slices.Contains(s.categories.Broadcastable(), category) {
		return Route{}, ErrNotBroadcastable
	}
	if s.channel.DestinationKeys == nil || !slices.Contains(s.channel.DestinationKeys(), destinationKey) {
		return Route{}, ErrUnknownDestination
	}
	if correlationID == "" {
		correlationID = "teams-route"
	}
	var r Route
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO platform.notification_channel_routes(channel, category, destination_key, created_by)
			VALUES ($1, $2, $3, $4::uuid)
			ON CONFLICT ON CONSTRAINT notification_channel_routes_unique DO NOTHING
			RETURNING id::text, category, destination_key, created_by::text, created_at`,
			ChannelTeams, category, destinationKey, actorID).Scan(&r.ID, &r.Category, &r.DestinationKey, &r.CreatedBy, &r.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRouteExists
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Change{Action: "notifications.teams.route_created", TargetType: "notification_channel_route", TargetID: r.ID,
			Actor: audit.UserActor(actorID), CorrelationID: correlationID,
			After: map[string]any{"category": category, "destinationKey": destinationKey}})
	})
	if err != nil {
		if errors.Is(err, ErrRouteExists) {
			return Route{}, err
		}
		return Route{}, fmt.Errorf("create channel route: %w", err)
	}
	return r, nil
}

// DeleteRoute removes a route and audits it. Posts already created for it are cancelled when they are sent.
func (s *Service) DeleteRoute(ctx context.Context, actorID string, canManage bool, id, correlationID string) error {
	if actorID == "" || !canManage {
		return ErrForbidden
	}
	if !validUUID(id) {
		return ErrRouteNotFound
	}
	if correlationID == "" {
		correlationID = "teams-route"
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var category, key string
		err := tx.QueryRow(ctx, `
			DELETE FROM platform.notification_channel_routes WHERE id = $1::uuid AND channel = $2
			RETURNING category, destination_key`, id, ChannelTeams).Scan(&category, &key)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRouteNotFound
		}
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Change{Action: "notifications.teams.route_deleted", TargetType: "notification_channel_route", TargetID: id,
			Actor: audit.UserActor(actorID), CorrelationID: correlationID,
			Before: map[string]any{"category": category, "destinationKey": key}})
	})
	if err != nil {
		if errors.Is(err, ErrRouteNotFound) {
			return err
		}
		return fmt.Errorf("delete channel route: %w", err)
	}
	return nil
}
