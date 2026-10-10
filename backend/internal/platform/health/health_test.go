package health

import (
	"context"
	"testing"
	"time"
)

func TestRegistryRunsCachesAndSorts(t *testing.T) {
	r := NewRegistry()
	calls := 0
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	_ = r.Register(Check{Key: "b", Category: CategoryIntegration, Run: func(context.Context) Result { calls++; return Result{Status: StatusFake} }})
	_ = r.Register(Check{Key: "a", Category: CategorySystem, Run: func(context.Context) Result { calls++; return Result{Status: StatusOK} }})
	if err := r.Register(Check{Key: "a", Run: func(context.Context) Result { return Result{} }}); err == nil {
		t.Fatal("duplicate key must fail")
	}
	got := r.All(context.Background())
	if len(got) != 2 || got[0].Key != "b" || got[1].Key != "a" { // integration < system alphabetically
		t.Fatalf("order: %+v", got)
	}
	r.All(context.Background())
	if calls != 2 {
		t.Fatalf("second call within ttl must be cached: %d", calls)
	}
	now = now.Add(6 * time.Second)
	r.All(context.Background())
	if calls != 4 {
		t.Fatalf("after ttl checks run again: %d", calls)
	}
}

func TestRegistryContainsPanicsAndInvalidStatus(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(Check{Key: "boom", Run: func(context.Context) Result { panic("secret=hunter2") }})
	_ = r.Register(Check{Key: "bad", Run: func(context.Context) Result { return Result{Status: "weird"} }})
	for _, e := range r.All(context.Background()) {
		if e.Status != StatusFailing || e.ErrorCode == "" {
			t.Errorf("%s: %+v", e.Key, e.Result)
		}
		if e.ErrorCode == "secret=hunter2" {
			t.Error("panic text must not leak")
		}
	}
}

func TestFreshStatus(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	h := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	for name, tc := range map[string]struct {
		success, failure *time.Time
		want             Status
	}{
		"fresh":              {h(time.Hour), nil, StatusOK},
		"stale":              {h(4 * time.Hour), nil, StatusStale},
		"none":               {nil, nil, StatusUnknown},
		"failure newer":      {h(2 * time.Hour), h(time.Hour), StatusFailing},
		"failure older":      {h(time.Hour), h(2 * time.Hour), StatusOK},
		"failure no success": {nil, h(time.Hour), StatusFailing},
	} {
		if got := FreshStatus(now, time.Hour, tc.success, tc.failure); got != tc.want {
			t.Errorf("%s: %s want %s", name, got, tc.want)
		}
	}
}
