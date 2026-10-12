package authentication

import (
	"testing"
	"time"
)

func TestRecentEntraMFA(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-5 * time.Minute)
	stale := now.Add(-16 * time.Minute)
	future := now.Add(time.Second)
	for _, tc := range []struct {
		name      string
		amr, acrs []string
		when      *time.Time
		want      bool
	}{
		{"mfa", []string{"pwd", "mfa"}, nil, &recent, true},
		{"context", nil, []string{"c1"}, &recent, true},
		{"missing method", nil, nil, &recent, false},
		{"missing time", []string{"mfa"}, nil, nil, false},
		{"stale", []string{"mfa"}, nil, &stale, false},
		{"future", []string{"mfa"}, nil, &future, false},
		{"wrong context", nil, []string{"c2"}, &recent, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RecentEntraMFA(tc.amr, tc.acrs, tc.when, "c1", 15*time.Minute, now); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
