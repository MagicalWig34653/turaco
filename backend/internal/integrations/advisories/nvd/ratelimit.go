package nvd

import (
	"context"
	"sync"
	"time"
)

// Clock abstracts time so the rate limiter and the backoff are testable without sleeping.
type Clock interface {
	Now() time.Time
	// Sleep waits for d or until ctx is done (then it returns the context error).
	Sleep(ctx context.Context, d time.Duration) error
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// limiter allows at most limit requests in any rolling window (NVD: 5 per 30 s without an API key, 50
// with one). It is conservative: it counts every attempt, including retries.
type limiter struct {
	mu     sync.Mutex
	clock  Clock
	limit  int
	window time.Duration
	sent   []time.Time
}

func newLimiter(clock Clock, limit int, window time.Duration) *limiter {
	return &limiter{clock: clock, limit: limit, window: window}
}

// wait blocks until a request may be sent and records it.
func (l *limiter) wait(ctx context.Context) error {
	for {
		l.mu.Lock()
		now := l.clock.Now()
		cut := 0
		for cut < len(l.sent) && !l.sent[cut].After(now.Add(-l.window)) {
			cut++
		}
		l.sent = l.sent[cut:]
		if len(l.sent) < l.limit {
			l.sent = append(l.sent, now)
			l.mu.Unlock()
			return nil
		}
		d := l.sent[0].Add(l.window).Sub(now)
		l.mu.Unlock()
		if err := l.clock.Sleep(ctx, d); err != nil {
			return err
		}
	}
}
