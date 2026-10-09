package query

import (
	"sync"
	"time"
)

const (
	// limiterMaxBuckets is the hard upper bound of tracked keys. The map never grows beyond it, whatever the
	// callers do.
	limiterMaxBuckets = 10000
	// limiterEvictScan bounds the work of one eviction: at most this many entries are inspected.
	limiterEvictScan = 64
	// limiterIdle is how long a bucket must be untouched to count as idle (a full bucket again).
	limiterIdle = 10 * time.Minute
)

// Limiter is a per-key token bucket (key: the principal id). It is in memory
// per process, which bounds each API instance; it is not a quota system.
//
// Storage is bounded: at most limiterMaxBuckets keys are tracked and an
// insertion at the limit evicts after inspecting at most limiterEvictScan
// entries (idle ones first, otherwise the least recently used of the sample).
// A key that is evicted while it is still active starts over with a full
// bucket, which can only give it back a burst, never more than burst tokens.
type Limiter struct {
	mu         sync.Mutex
	rate       float64 // tokens per second
	burst      float64
	buckets    map[string]*bucket
	maxBuckets int
	now        func() time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

// NewLimiter allows ratePerSecond sustained and burst requests at once.
func NewLimiter(ratePerSecond float64, burst int) *Limiter {
	return &Limiter{rate: ratePerSecond, burst: float64(burst), buckets: map[string]*bucket{}, maxBuckets: limiterMaxBuckets, now: time.Now}
}

// Allow takes one token for key.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		l.makeRoom(now)
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

// size is the number of tracked keys (tests).
func (l *Limiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// makeRoom keeps len(buckets) below maxBuckets before an insertion. It
// inspects at most limiterEvictScan entries (Go map iteration order is
// unspecified, which makes the sample arbitrary), removes the idle ones and,
// if none was idle, the least recently used of the sample. The caller holds mu.
func (l *Limiter) makeRoom(now time.Time) {
	if len(l.buckets) < l.maxBuckets {
		return
	}
	var oldestKey string
	var oldestAt time.Time
	scanned, removed := 0, 0
	for k, b := range l.buckets {
		if scanned == limiterEvictScan {
			break
		}
		scanned++
		if now.Sub(b.at) > limiterIdle {
			delete(l.buckets, k)
			removed++
			continue
		}
		if oldestKey == "" || b.at.Before(oldestAt) {
			oldestKey, oldestAt = k, b.at
		}
	}
	if removed == 0 && oldestKey != "" {
		delete(l.buckets, oldestKey)
	}
}
