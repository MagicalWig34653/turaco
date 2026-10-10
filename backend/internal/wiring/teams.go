package wiring

import (
	"context"
	"errors"
	"fmt"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/microsoft"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/teams"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/modules"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

// TeamsModuleKey is the optional module that switches Teams channel posts.
const TeamsModuleKey = "teams"

// TeamsSender builds the Teams adapter: NotConfigured without a destinations file, otherwise the Workflows adapter.
// A file that is present but invalid stops the process at startup instead of failing every post later.
func TeamsSender(cfg config.TeamsConfig) (teams.Sender, error) {
	if !cfg.Configured() {
		return teams.NotConfigured{}, nil
	}
	dest, err := teams.LoadDestinations(cfg.DestinationsFile)
	if err != nil {
		return nil, err
	}
	w, err := teams.NewWorkflows(dest, microsoft.Config{HTTPProxy: cfg.HTTPProxy, CAFile: cfg.CAFile})
	if err != nil {
		return nil, fmt.Errorf("set up Teams channel client: %w", err)
	}
	return w, nil
}

// TeamsChannelOptions connects the Notification service to the Teams adapter and the module switch.
func TeamsChannelOptions(sender teams.Sender, mods *modules.Service) notifications.ChannelOptions {
	return notifications.ChannelOptions{
		Enabled:         func(ctx context.Context) (bool, error) { return mods.Enabled(ctx, TeamsModuleKey) },
		DestinationKeys: sender.DestinationKeys,
		Mode:            string(sender.Mode()),
	}
}

// TeamsChannelPoster adapts a Teams Sender to the Notification service's channel port and translates the adapter's
// error classes, so the platform never sees integration types.
type TeamsChannelPoster struct{ Sender teams.Sender }

// PostToChannel implements notifications.ChannelPoster.
func (p TeamsChannelPoster) PostToChannel(ctx context.Context, destinationKey string, c notifications.ChannelCard) error {
	err := p.Sender.PostToChannel(ctx, destinationKey, teams.Card{Kind: c.Kind, Category: c.Category, Reference: c.Reference,
		Headline: c.Headline, Action: c.Action, LinkURL: c.LinkURL, Locale: c.Locale})
	if err == nil {
		return nil
	}
	var transient *teams.TransientError
	if errors.As(err, &transient) {
		return &notifications.TransientChannelError{Code: transient.Code, RetryAfter: transient.RetryAfter}
	}
	// Everything else (not configured, unknown destination, invalid card, rejected by Teams) cannot be fixed by
	// retrying the same post.
	return &notifications.PermanentChannelError{Code: teams.ErrorCode(err)}
}

var _ notifications.ChannelPoster = TeamsChannelPoster{}
