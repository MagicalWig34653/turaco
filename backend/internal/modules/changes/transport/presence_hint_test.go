package transport

import (
	"context"
	"testing"
	"time"

	presencepublic "github.com/MagicalWig34653/turaco/backend/internal/modules/presence/public"
)

type windowPresence struct {
	viewer presencepublic.Viewer
	result presencepublic.WindowResult
}

func (p *windowPresence) AvailabilityWindow(_ context.Context, viewer presencepublic.Viewer, _ string, _, _ time.Time, _ presencepublic.Need) (presencepublic.WindowResult, error) {
	p.viewer = viewer
	return p.result, nil
}

func TestOwnerWindowHintPrivacyAndDisable(t *testing.T) {
	now := time.Now().UTC()
	p := &windowPresence{result: presencepublic.WindowResult{Segments: []presencepublic.Segment{{From: now, To: now.Add(time.Hour), Value: presencepublic.Unknown, Explanation: "out_of_scope"}}}}
	h := &handler{presence: p}
	viewer := presencepublic.Viewer{UserID: "viewer"}
	if got := h.ownerWindowHint(context.Background(), viewer, "owner", now, now.Add(time.Hour), now); got != nil {
		t.Fatalf("scope denial leaked a hint: %+v", got)
	}
	if p.viewer.ViewAvailability || p.viewer.UserID != "viewer" {
		t.Fatalf("viewer authority changed: %+v", p.viewer)
	}
	p.result = presencepublic.WindowResult{Disabled: true}
	if got := h.ownerWindowHint(context.Background(), viewer, "owner", now, now.Add(time.Hour), now); got != nil {
		t.Fatalf("disabled module leaked a hint: %+v", got)
	}
	p.result = presencepublic.WindowResult{Segments: []presencepublic.Segment{{From: now, To: now.Add(time.Hour), Value: presencepublic.Unavailable, Explanation: "unavailable_entry", Sources: []presencepublic.SourceInfo{{Source: "manual", Freshness: "manual"}}}}}
	viewer.ViewAvailability = true
	got := h.ownerWindowHint(context.Background(), viewer, "owner", now, now.Add(time.Hour), now)
	if got == nil || len(got.Segments) != 1 || got.Segments[0].Value != presencepublic.Unavailable || len(got.Segments[0].Sources) != 1 || !p.viewer.ViewAvailability {
		t.Fatalf("authorized hint: %+v", got)
	}
}
