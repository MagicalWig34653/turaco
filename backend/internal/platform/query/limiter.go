package query

import (
	"sync"
	"time"
)

// Limiter is a per-key token bucket (key: the principal id). It is in memory
// per process, which bounds each API instance; it is not a quota system.
type Limiter struct {
	mu      sync.Mutex
	rate    float64 // tokens per second
	burst   float64
	buckets map[string]*bucket
	now     func() time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

// NewLimiter allows ratePerSecond sustained and burst requests at once.
func NewLimiter(ratePerSecond float64, burst int) *Limiter {
	return &Limiter{rate: ratePerSecond, burst: float64(burst), buckets: map[string]*bucket{}, now: time.Now}
}

// Allow takes one token for key.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.buckets) > 10000 {
		for k, b := range l.buckets {
			if now.Sub(b.at) > 10*time.Minute {
				delete(l.buckets, k)
			}
		}
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, at: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.at).Seconds()*l.rate)
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
