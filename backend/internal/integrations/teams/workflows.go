package teams

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/microsoft"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/providerstatus"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/health"
)

// Workflows posts Adaptive Cards to Teams Workflows webhook URLs ("Post to a channel when a webhook request is
// received"). It is written from the documentation and has not been exercised against a live tenant, which is why
// Status stays "unverified" until a post succeeded in this process.
type Workflows struct {
	client  *microsoft.Client
	dest    Destinations
	tracker *providerstatus.Tracker
}

// NewWorkflows builds the real adapter. cfg carries the optional proxy and CA file; the host allow-list is derived
// from the destinations, so only the configured webhook hosts can be called.
func NewWorkflows(dest Destinations, cfg microsoft.Config) (*Workflows, error) {
	if len(dest) == 0 {
		return nil, errors.New("teams: no destination configured")
	}
	cfg.AllowedHosts = dest.Hosts()
	client, err := microsoft.New(cfg)
	if err != nil {
		return nil, err
	}
	return &Workflows{client: client, dest: dest, tracker: providerstatus.NewTracker(nil)}, nil
}

// Mode implements Sender.
func (w *Workflows) Mode() health.Mode { return health.ModeReal }

// Status implements providerstatus.Reporter.
func (w *Workflows) Status() providerstatus.Snapshot { return w.tracker.Status() }

// DestinationKeys implements Sender.
func (w *Workflows) DestinationKeys() []string { return w.dest.Keys() }

// PostToChannel implements Sender.
func (w *Workflows) PostToChannel(ctx context.Context, destinationKey string, card Card) error {
	if err := card.Validate(); err != nil {
		return err
	}
	target, ok := w.dest[destinationKey]
	if !ok {
		return ErrUnknownDestination
	}
	body, err := json.Marshal(renderMessage(card))
	if err != nil {
		return fmt.Errorf("teams: render card: %w", err)
	}
	resp, err := w.client.Do(ctx, http.MethodPost, target, bytes.NewReader(body), http.Header{"Content-Type": {"application/json"}})
	if err != nil {
		var te *microsoft.TransientError
		switch {
		case errors.As(err, &te):
			out := classifyTransient(te)
			w.tracker.Failure(out.Code)
			return out
		case errors.Is(err, microsoft.ErrHostNotAllowed):
			w.tracker.Failure("host_not_allowed")
			return &PermanentError{Code: "host_not_allowed"}
		case errors.Is(err, microsoft.ErrResponseTooLarge):
			w.tracker.Failure("response_too_large")
			return &PermanentError{Code: "response_too_large"}
		}
		w.tracker.Failure("error")
		return &TransientError{Code: "error"}
	}
	if resp.Status >= 200 && resp.Status < 300 {
		w.tracker.Success()
		return nil
	}
	code := "http_" + strconv.Itoa(resp.Status)
	w.tracker.Failure(code)
	// 3xx (a redirect is never followed) and 4xx other than 429: the flow was removed, switched off or the
	// signature in the URL no longer matches. Retrying cannot fix it.
	return &PermanentError{Code: code}
}

func classifyTransient(te *microsoft.TransientError) *TransientError {
	switch {
	case te.Status == http.StatusTooManyRequests:
		return &TransientError{Code: "rate_limited", RetryAfter: te.RetryAfter}
	case te.Status >= 500:
		return &TransientError{Code: "http_" + strconv.Itoa(te.Status), RetryAfter: te.RetryAfter}
	default:
		return &TransientError{Code: "network"}
	}
}

// renderMessage builds the Workflows message envelope with one reference-only Adaptive Card.
func renderMessage(c Card) map[string]any {
	card := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard",
		"version": "1.4",
		"body": []any{
			map[string]any{"type": "TextBlock", "text": c.Headline, "weight": "Bolder", "wrap": true},
			map[string]any{"type": "TextBlock", "text": c.Reference, "isSubtle": true, "wrap": true},
		},
		"actions": []any{
			map[string]any{"type": "Action.OpenUrl", "title": c.Action, "url": c.LinkURL},
		},
	}
	return map[string]any{
		"type": "message",
		"attachments": []any{
			map[string]any{"contentType": "application/vnd.microsoft.card.adaptive", "contentUrl": nil, "content": card},
		},
	}
}
