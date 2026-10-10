package osv

import (
	"context"
	"time"
)

// Clock abstracts time so the retry backoff is testable without sleeping.
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
