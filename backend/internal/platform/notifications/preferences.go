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

// Preferences returns the User's email preference for every registered
// category (enabled unless the User opted out), sorted by category.
func (s *Service) Preferences(ctx context.Context, userID string) ([]Preference, error) {
	names := s.categories.Names()
	out := make([]Preference, 0, len(names))
	for _, c := range names {
		out = append(out, Preference{Category: c, Channel: ChannelEmail, Enabled: true})
	}
	if validUUID(userID) {
		rows, err := s.pool.Query(ctx, `
			SELECT category, enabled FROM platform.notification_preferences
			WHERE user_id = $1::uuid AND channel = $2`, userID, ChannelEmail)
		if err != nil {
			return nil, fmt.Errorf("list notification preferences: %w", err)
		}
		defer rows.Close()
		disabled := map[string]bool{}
		for rows.Next() {
			var category string
			var enabled bool
			if err := rows.Scan(&category, &enabled); err != nil {
				return nil, fmt.Errorf("list notification preferences: scan: %w", err)
			}
			disabled[category] = !enabled
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("list notification preferences: %w", err)
		}
		for i := range out {
			if disabled[out[i].Category] {
				out[i].Enabled = false
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Category < out[j].Category })
	return out, nil
}

// SetEmailPreference stores the User's email choice for a category.
func (s *Service) SetEmailPreference(ctx context.Context, userID, category string, enabled bool) error {
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
		userID, category, ChannelEmail, enabled)
	if err != nil {
		return fmt.Errorf("set notification preference: %w", err)
	}
	return nil
}
