package notifications

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// ChannelEmail is the only channel with preferences in F2.
const ChannelEmail = "email"

// ErrUnknownCategory is returned for a category that is not registered.
var ErrUnknownCategory = errors.New("notifications: unknown category")

// Preference is a User's choice for one category and channel.
type Preference struct {
	Category string
	Channel  string
	Enabled  bool
}

// Preferences returns the User's preference for every registered category on the email channel and for every category
// with a personal Teams card on the Teams channel (enabled unless the User opted out), sorted by category and channel.
func (s *Service) Preferences(ctx context.Context, userID string) ([]Preference, error) {
	names := s.categories.Names()
	out := make([]Preference, 0, len(names))
	for _, c := range names {
		out = append(out, Preference{Category: c, Channel: ChannelEmail, Enabled: true})
	}
	for _, c := range s.categories.TeamsCardCategories() {
		out = append(out, Preference{Category: c, Channel: ChannelTeamsPersonal, Enabled: true})
	}
	if validUUID(userID) {
		rows, err := s.pool.Query(ctx, `
			SELECT category, channel, enabled FROM platform.notification_preferences
			WHERE user_id = $1::uuid`, userID)
		if err != nil {
			return nil, fmt.Errorf("list notification preferences: %w", err)
		}
		defer rows.Close()
		disabled := map[[2]string]bool{}
		for rows.Next() {
			var category, channel string
			var enabled bool
			if err := rows.Scan(&category, &channel, &enabled); err != nil {
				return nil, fmt.Errorf("list notification preferences: scan: %w", err)
			}
			disabled[[2]string{category, channel}] = !enabled
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("list notification preferences: %w", err)
		}
		for i := range out {
			if disabled[[2]string{out[i].Category, out[i].Channel}] {
				out[i].Enabled = false
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Channel < out[j].Channel
	})
	return out, nil
}

// SetEmailPreference stores the User's email choice for a category.
func (s *Service) SetEmailPreference(ctx context.Context, userID, category string, enabled bool) error {
	return s.setPreference(ctx, userID, category, ChannelEmail, enabled)
}

// SetTeamsPreference stores the User's choice for the personal Teams card of a category; only categories that carry
// a card have one.
func (s *Service) SetTeamsPreference(ctx context.Context, userID, category string, enabled bool) error {
	if cat, ok := s.categories.Lookup(category); !ok || cat.TeamsCard == nil {
		return ErrUnknownCategory
	}
	return s.setPreference(ctx, userID, category, ChannelTeamsPersonal, enabled)
}

func (s *Service) setPreference(ctx context.Context, userID, category, channel string, enabled bool) error {
	if !validUUID(userID) {
		return errors.New("notifications: user id is required")
	}
	if !s.categories.Valid(category) {
		return ErrUnknownCategory
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO platform.notification_preferences(user_id, category, channel, enabled)
		VALUES ($1::uuid, $2, $3, $4)
		ON CONFLICT (user_id, category, channel) DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = now()`,
		userID, category, channel, enabled)
	if err != nil {
		return fmt.Errorf("set notification preference: %w", err)
	}
	return nil
}
