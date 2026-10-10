package teams

import (
	"context"
	"sort"
	"sync"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/health"
)

// Posted is one card the Fake accepted.
type Posted struct {
	DestinationKey string
	Card           Card
}

// Fake is an in-memory Sender for tests: it records cards and injects failures (a scripted error queue, which
// can hold *TransientError values with a RetryAfter to simulate a 429).
type Fake struct {
	mu     sync.Mutex
	keys   map[string]bool
	posted []Posted
	fail   []error
}

// NewFake creates a Fake that knows the destination keys.
func NewFake(keys ...string) *Fake {
	f := &Fake{keys: map[string]bool{}}
	for _, k := range keys {
		f.keys[k] = true
	}
	return f
}

// FailNext queues errors; each PostToChannel call consumes one and returns it instead of posting.
func (f *Fake) FailNext(errs ...error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = append(f.fail, errs...)
}

// Posted returns a copy of the recorded posts.
func (f *Fake) Posted() []Posted {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Posted(nil), f.posted...)
}

// Mode implements Sender.
func (f *Fake) Mode() health.Mode { return health.ModeFake }

// DestinationKeys implements Sender.
func (f *Fake) DestinationKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.keys))
	for k := range f.keys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// PostToChannel implements Sender with the same validation as the real adapter.
func (f *Fake) PostToChannel(_ context.Context, destinationKey string, card Card) error {
	if err := card.Validate(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.keys[destinationKey] {
		return ErrUnknownDestination
	}
	if len(f.fail) > 0 {
		err := f.fail[0]
		f.fail = f.fail[1:]
		return err
	}
	f.posted = append(f.posted, Posted{DestinationKey: destinationKey, Card: card})
	return nil
}

// NotConfigured is the Sender of an installation without destinations: every post fails with ErrNotConfigured.
type NotConfigured struct{}

// Mode implements Sender.
func (NotConfigured) Mode() health.Mode { return health.ModeNotConfigured }

// DestinationKeys implements Sender.
func (NotConfigured) DestinationKeys() []string { return nil }

// PostToChannel implements Sender.
func (NotConfigured) PostToChannel(context.Context, string, Card) error { return ErrNotConfigured }
