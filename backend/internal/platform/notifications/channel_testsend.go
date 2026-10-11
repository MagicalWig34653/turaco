package notifications

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// ActionTeamsTestSent is the audit action of an explicit test send; its metadata carries the destination key, the
// outcome and the machine error code, never a URL or provider text.
const ActionTeamsTestSent = "notifications.teams.test_sent"

// Test send admission: at most TestSendsPerWindow per destination within TestSendWindow, counted from the audit
// events so the limit holds across API instances and restarts. Failed attempts count too, so a broken destination
// is not hammered.
const (
	TestSendsPerWindow = 5
	TestSendWindow     = 10 * time.Minute
	testSendTimeout    = 15 * time.Second
)

var (
	// ErrTestSendRateLimited means the destination received its allowed test sends recently.
	ErrTestSendRateLimited = errors.New("notifications: test send rate limited")
	// ErrTestSendUnavailable means the service has no channel poster or no base URL to build the link of the card.
	ErrTestSendUnavailable = errors.New("notifications: test send unavailable")
)

// TestSendResult is the outcome of one test send. ErrorCode is a short machine code of the adapter.
type TestSendResult struct {
	Delivered bool
	ErrorCode string
}

// testCardText is the generic wording of the test card; it carries no record content, like every channel post.
var testCardText = map[string]ChannelText{
	"en": {Headline: "Test message: this channel is connected to Turaco.", Action: "Open in Turaco"},
	"de": {Headline: "Testnachricht: Dieser Kanal ist mit Turaco verbunden.", Action: "In Turaco öffnen"},
}

// TestSendChannel posts one clearly marked test card to a configured destination so an administrator can verify a
// webhook before routing categories to it. It is an explicit, audited operation of integrations.teams.manage, works
// while the module is off (preparation), and is rate limited per destination. The card is reference-only like every
// post: category "teams.test", reference "TURACO-TEST", a link to the administration screen. A failed post is a
// result (Delivered=false with a code), not an error; it is audited as well.
func (s *Service) TestSendChannel(ctx context.Context, actorID string, canManage bool, destinationKey, correlationID string) (TestSendResult, error) {
	if actorID == "" || !canManage {
		return TestSendResult{}, ErrForbidden
	}
	if s.channel.DestinationKeys == nil || !slices.Contains(s.channel.DestinationKeys(), destinationKey) {
		return TestSendResult{}, ErrUnknownDestination
	}
	baseURL := strings.TrimRight(s.channel.BaseURL, "/")
	if s.channel.Poster == nil || baseURL == "" {
		return TestSendResult{}, ErrTestSendUnavailable
	}
	locale := "en"
	if s.channel.Locale == "de" {
		locale = "de"
	}
	if correlationID == "" {
		correlationID = "teams-test-send"
	}
	text := testCardText[locale]
	card := ChannelCard{Kind: PostKindUpdated, Category: "teams.test", Reference: "TURACO-TEST", Headline: text.Headline,
		Action: text.Action, LinkURL: baseURL + "/admin/teams-channels", Locale: locale}

	var res TestSendResult
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// The lock serializes test sends of one destination, so parallel requests cannot all pass the count.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('teams-test-send:' || $1, 0))`, destinationKey); err != nil {
			return fmt.Errorf("lock test send: %w", err)
		}
		var used int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.audit_events
			WHERE action = $1 AND target_type = 'teams_channel_destination' AND target_id = $2
			  AND occurred_at > now() - make_interval(secs => $3)`,
			ActionTeamsTestSent, destinationKey, TestSendWindow.Seconds()).Scan(&used); err != nil {
			return fmt.Errorf("count test sends: %w", err)
		}
		if used >= TestSendsPerWindow {
			return ErrTestSendRateLimited
		}
		pctx, cancel := context.WithTimeout(ctx, testSendTimeout)
		postErr := s.channel.Poster.PostToChannel(pctx, destinationKey, card)
		cancel()
		res = TestSendResult{Delivered: postErr == nil}
		if postErr != nil {
			res.ErrorCode = channelErrorCode(postErr)
		}
		return audit.Record(ctx, tx, audit.Change{Action: ActionTeamsTestSent, TargetType: "teams_channel_destination", TargetID: destinationKey,
			Actor: audit.UserActor(actorID), CorrelationID: correlationID,
			Metadata: map[string]any{"destinationKey": destinationKey, "delivered": res.Delivered, "errorCode": res.ErrorCode}})
	})
	if err != nil {
		if errors.Is(err, ErrTestSendRateLimited) {
			return TestSendResult{}, err
		}
		return TestSendResult{}, fmt.Errorf("test send: %w", err)
	}
	return res, nil
}

// channelErrorCode reduces a poster error to its stored machine code.
func channelErrorCode(err error) string {
	var transient *TransientChannelError
	var permanent *PermanentChannelError
	switch {
	case errors.As(err, &transient):
		return transient.Code
	case errors.As(err, &permanent):
		return permanent.Code
	default:
		return "error"
	}
}
