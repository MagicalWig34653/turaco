package notifications_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

func TestChannelSenderLimitsPostsPerDestinationAndDefersTheRest(t *testing.T) {
	f := newChannelFixture(t)
	f.sender.WithRateLimit(2)
	f.route(f.keyA)
	f.route(f.keyB)
	for i := 0; i < 3; i++ {
		if n, err := f.post(fmt.Sprintf("ev%d", i)); err != nil || n != 2 {
			t.Fatalf("post %d: n=%d err=%v", i, n, err)
		}
	}
	var a, b []chanDelivery
	for _, d := range f.deliveries() {
		if d.Key == f.keyA {
			a = append(a, d)
		} else {
			b = append(b, d)
		}
	}
	for i, d := range a[:2] {
		if err := f.handle(d, 1); err != nil {
			t.Fatalf("destination A post %d: %v", i, err)
		}
	}
	err := f.handle(a[2], 1)
	if !jobs.IsDeferred(err) {
		t.Fatalf("third post to the same destination must be deferred: %v", err)
	}
	for _, d := range f.deliveries() {
		if d.ID == a[2].ID && (d.Status != "pending" || d.Attempts != 0) {
			t.Errorf("deferred delivery = %+v, want pending with the attempt refunded", d)
		}
	}
	if len(f.poster.cards) != 2 {
		t.Errorf("posted %d cards, want 2", len(f.poster.cards))
	}
	// Another destination has its own budget.
	for i, d := range b[:2] {
		if err := f.handle(d, 1); err != nil {
			t.Fatalf("destination B post %d: %v", i, err)
		}
	}
	// The limit is a window: once the earlier posts are older than a minute the deferred post goes out.
	if _, err := f.pool.Exec(context.Background(), `UPDATE platform.notification_deliveries SET updated_at = now() - interval '2 minutes'
		WHERE channel = 'teams_channel' AND destination_key = $1 AND status = 'delivered'`, f.keyA); err != nil {
		t.Fatal(err)
	}
	if err := f.handle(a[2], 2); err != nil {
		t.Fatalf("after the window: %v", err)
	}
	if len(f.poster.cards) != 5 {
		t.Errorf("posted %d cards, want 5", len(f.poster.cards))
	}
}

type recordingPoster struct {
	mu    sync.Mutex
	cards []notifications.ChannelCard
	keys  []string
	err   error
}

func (p *recordingPoster) PostToChannel(_ context.Context, key string, c notifications.ChannelCard) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cards, p.keys = append(p.cards, c), append(p.keys, key)
	return p.err
}

func TestTestSendIsAuthorizedRateLimitedAndAudited(t *testing.T) {
	f := newChannelFixture(t)
	ctx := context.Background()
	poster := &recordingPoster{}
	opts := notifications.ChannelOptions{
		DestinationKeys: func() []string { return []string{f.keyA, f.keyB} }, Mode: "fake",
		Poster: poster, BaseURL: "https://turaco.example.org/", Locale: "de"}
	svc := notifications.NewService(f.pool, f.reg).WithChannelPosts(opts)
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE action = $1 AND target_id = ANY($2)`,
			notifications.ActionTeamsTestSent, []string{f.keyA, f.keyB})
	})

	if _, err := svc.TestSendChannel(ctx, f.actor, false, f.keyA, "c"); !errors.Is(err, notifications.ErrForbidden) {
		t.Errorf("without permission: %v", err)
	}
	if _, err := svc.TestSendChannel(ctx, "", true, f.keyA, "c"); !errors.Is(err, notifications.ErrForbidden) {
		t.Errorf("without actor: %v", err)
	}
	if _, err := svc.TestSendChannel(ctx, f.actor, true, "nope", "c"); !errors.Is(err, notifications.ErrUnknownDestination) {
		t.Errorf("unknown destination: %v", err)
	}
	noURL := notifications.NewService(f.pool, f.reg).WithChannelPosts(notifications.ChannelOptions{
		DestinationKeys: opts.DestinationKeys, Poster: poster})
	if _, err := noURL.TestSendChannel(ctx, f.actor, true, f.keyA, "c"); !errors.Is(err, notifications.ErrTestSendUnavailable) {
		t.Errorf("without base URL: %v", err)
	}
	if len(poster.cards) != 0 {
		t.Fatalf("refused test sends must not post: %d", len(poster.cards))
	}

	for i := 0; i < notifications.TestSendsPerWindow; i++ {
		res, err := svc.TestSendChannel(ctx, f.actor, true, f.keyA, "c")
		if err != nil || !res.Delivered {
			t.Fatalf("test send %d: %+v %v", i, res, err)
		}
	}
	c := poster.cards[0]
	if c.Reference != "TURACO-TEST" || c.LinkURL != "https://turaco.example.org/admin/teams-channels" || c.Locale != "de" ||
		!strings.HasPrefix(c.Headline, "Testnachricht") || poster.keys[0] != f.keyA {
		t.Errorf("card = %+v key=%s", c, poster.keys[0])
	}
	if _, err := svc.TestSendChannel(ctx, f.actor, true, f.keyA, "c"); !errors.Is(err, notifications.ErrTestSendRateLimited) {
		t.Fatalf("over the limit: %v", err)
	}
	if len(poster.cards) != notifications.TestSendsPerWindow {
		t.Errorf("a limited test send must not post: %d", len(poster.cards))
	}
	// The limit is per destination, and a failing post is a result that is audited and counted.
	poster.err = &notifications.PermanentChannelError{Code: "http_404"}
	res, err := svc.TestSendChannel(ctx, f.actor, true, f.keyB, "c")
	if err != nil || res.Delivered || res.ErrorCode != "http_404" {
		t.Fatalf("failing destination: %+v %v", res, err)
	}
	var okN, failN int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE metadata->>'delivered' = 'true'), count(*) FILTER (WHERE metadata->>'delivered' = 'false')
		FROM platform.audit_events WHERE action = $1 AND target_id = ANY($2) AND actor_id = $3::uuid AND metadata::text NOT LIKE '%https://%'`,
		notifications.ActionTeamsTestSent, []string{f.keyA, f.keyB}, f.actor).Scan(&okN, &failN); err != nil {
		t.Fatal(err)
	}
	if okN != notifications.TestSendsPerWindow || failN != 1 {
		t.Errorf("audit rows delivered=%d failed=%d", okN, failN)
	}
}

func TestTestSendParallelRequestsCannotExceedTheLimit(t *testing.T) {
	f := newChannelFixture(t)
	ctx := context.Background()
	poster := &recordingPoster{}
	svc := notifications.NewService(f.pool, f.reg).WithChannelPosts(notifications.ChannelOptions{
		DestinationKeys: func() []string { return []string{f.keyA} }, Poster: poster, BaseURL: "https://turaco.example.org"})
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE action = $1 AND target_id = $2`, notifications.ActionTeamsTestSent, f.keyA)
	})
	var wg sync.WaitGroup
	var mu sync.Mutex
	limited := 0
	for i := 0; i < notifications.TestSendsPerWindow+3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.TestSendChannel(ctx, f.actor, true, f.keyA, "c"); errors.Is(err, notifications.ErrTestSendRateLimited) {
				mu.Lock()
				limited++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(poster.cards) != notifications.TestSendsPerWindow || limited != 3 {
		t.Errorf("posted=%d limited=%d, want %d and 3", len(poster.cards), limited, notifications.TestSendsPerWindow)
	}
}
