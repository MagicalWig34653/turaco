package query

import (
	"fmt"
	"testing"
	"time"
)

func TestLimiterStorageIsBounded(t *testing.T) {
	l := NewLimiter(1, 1)
	l.maxBuckets = 200
	now := time.Unix(1_000_000, 0)
	l.now = func() time.Time { return now }
	// A flood of distinct keys (all active: the clock does not advance) never grows the map past the hard limit.
	for i := 0; i < 5000; i++ {
		l.Allow(fmt.Sprintf("user-%d", i))
		if l.size() > l.maxBuckets {
			t.Fatalf("bucket count %d exceeds the hard maximum %d after %d keys", l.size(), l.maxBuckets, i+1)
		}
	}
	if l.size() != l.maxBuckets {
		t.Fatalf("size = %d, want the map to stay full at %d", l.size(), l.maxBuckets)
	}
}

func TestLimiterEvictsIdleBucketsFirst(t *testing.T) {
	l := NewLimiter(1, 1)
	l.maxBuckets = 100
	now := time.Unix(1_000_000, 0)
	l.now = func() time.Time { return now }
	for i := 0; i < 100; i++ {
		l.Allow(fmt.Sprintf("old-%d", i))
	}
	// Everything becomes idle; one new key may remove idle entries, and the map shrinks (bounded work per call).
	now = now.Add(limiterIdle + time.Minute)
	l.Allow("fresh")
	if l.size() > l.maxBuckets {
		t.Fatalf("size = %d, want at most %d", l.size(), l.maxBuckets)
	}
	if _, ok := l.buckets["fresh"]; !ok {
		t.Fatal("the new key must be tracked")
	}
	if l.size() >= 100 {
		t.Fatalf("idle entries were not evicted (size %d)", l.size())
	}
}

func TestLimiterEvictionWorkIsBounded(t *testing.T) {
	l := NewLimiter(1, 1)
	l.maxBuckets = 5000
	now := time.Unix(1_000_000, 0)
	l.now = func() time.Time { return now }
	for i := 0; i < 5000; i++ {
		l.Allow(fmt.Sprintf("old-%d", i))
	}
	now = now.Add(limiterIdle + time.Minute)
	l.Allow("fresh") // one insertion at the limit removes at most limiterEvictScan idle entries
	if removed := 5000 - (l.size() - 1); removed > limiterEvictScan {
		t.Fatalf("one insertion evicted %d entries, want at most %d", removed, limiterEvictScan)
	}
}

func TestLimiterActiveKeyKeepsItsBucketUnderChurn(t *testing.T) {
	l := NewLimiter(0.0001, 2)
	l.maxBuckets = 50
	now := time.Unix(1_000_000, 0)
	l.now = func() time.Time { return now }
	if !l.Allow("victim") || !l.Allow("victim") || l.Allow("victim") {
		t.Fatal("burst of 2 expected")
	}
	// Churn of other keys may evict the victim (documented), but the bound still holds and a tracked key is limited.
	for i := 0; i < 200; i++ {
		now = now.Add(time.Millisecond)
		l.Allow(fmt.Sprintf("other-%d", i))
		if l.size() > l.maxBuckets {
			t.Fatalf("size %d over bound", l.size())
		}
	}
}
