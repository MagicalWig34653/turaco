package repository_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
)

func TestRequesterTicketFollowAndDenials(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tk := e.raise()
	if on, err := e.svc.Following(ctx, e.user(), tk.ID); err != nil || !on {
		t.Fatalf("default follow: %v %v", on, err)
	}
	if on, err := e.svc.SetFollowing(ctx, e.c(e.alice), e.user(), tk.ID, false); err != nil || on {
		t.Fatalf("unfollow: %v %v", on, err)
	}
	if on, err := e.svc.Following(ctx, e.user(), tk.ID); err != nil || on {
		t.Fatalf("read unfollow: %v %v", on, err)
	}
	if on, err := e.svc.SetFollowing(ctx, e.c(e.alice), e.user(), tk.ID, true); err != nil || !on {
		t.Fatalf("refollow: %v %v", on, err)
	}
	for _, p := range []application.Principal{{UserID: e.bob}, e.staff(), {UserID: e.viewer, External: true}} {
		if _, err := e.svc.Following(ctx, p, tk.ID); !errors.Is(err, application.ErrNotFound) {
			t.Errorf("read by non-requester: %v", err)
		}
		if _, err := e.svc.SetFollowing(ctx, e.c(p.UserID), p, tk.ID, false); !errors.Is(err, application.ErrNotFound) {
			t.Errorf("write by non-requester: %v", err)
		}
	}
	if _, err := e.svc.SetFollowing(ctx, e.c(e.alice), e.user(), e.otherBook, false); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("unknown ticket: %v", err)
	}
}

func TestUnfollowSuppressesOnlyRequesterVisibleNotifications(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tk := e.raise()
	if _, err := e.svc.SetFollowing(ctx, e.c(e.alice), e.user(), tk.ID, false); err != nil {
		t.Fatal(err)
	}
	n := &capture{}
	cons := application.NewConsumers(repository.New(e.pool), dir{active: map[string]bool{e.alice: true, e.bob: true, e.agent: true}}, n)
	payload, _ := json.Marshal(map[string]any{"ticketId": tk.ID, "authorId": e.agent, "internal": false})
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := cons.OnCommentAdded(ctx, tx, events.OutboxEvent{ID: e.corr, EventType: "TicketCommentAdded", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if len(n.intents) != 0 {
		t.Fatalf("unfollowed requester received public reply: %+v", n.intents)
	}
	if _, err := e.svc.SetFollowing(ctx, e.c(e.alice), e.user(), tk.ID, true); err != nil {
		t.Fatal(err)
	}
	// A fresh transaction sees the committed preference.
	_ = tx.Rollback(ctx)
	tx, err = e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := cons.OnCommentAdded(ctx, tx, events.OutboxEvent{ID: e.corr + "-2", EventType: "TicketCommentAdded", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	if len(n.intents) != 1 || n.intents[0].RecipientUserID != e.alice {
		t.Fatalf("refollow notification: %+v", n.intents)
	}
}
