// Package providerstatus records whether an outbound provider client has ever been observed working. The real
// provider clients (Microsoft Graph, Autotask) are written strictly from the vendor documentation and have not
// been exercised against a live service; their status is "unverified" until a successful call was observed in
// this process. The tracker holds no tenant data: an error code and two timestamps.
package providerstatus

import (
	"sync"
	"time"
)

// State is the verification state of a client.
type State string

const (
	// Unverified: no successful call has been observed yet (the default; also after a restart).
	Unverified State = "unverified"
	// Verified: the last observed call succeeded.
	Verified State = "verified"
	// Failing: the last observed call failed.
	Failing State = "failing"
)

// Snapshot is a point-in-time copy of a Tracker.
type Snapshot struct {
	State         State
	LastSuccessAt *time.Time
	LastFailureAt *time.Time
	// LastErrorCode is a short machine code such as http_403 or auth_invalid_client; never a message or body.
	LastErrorCode string
}

// Reporter is implemented by clients that expose their verification state to the health checks.
type Reporter interface {
	Status() Snapshot
}

// Tracker is a concurrency-safe Reporter fed by the client after each call.
type Tracker struct {
	mu   sync.Mutex
	now  func() time.Time
	snap Snapshot
}

// NewTracker returns an unverified tracker. now may be nil (time.Now).
func NewTracker(now func() time.Time) *Tracker {
	if now == nil {
		now = time.Now
	}
	return &Tracker{now: now, snap: Snapshot{State: Unverified}}
}

// Success records an observed successful call.
func (t *Tracker) Success() {
	t.mu.Lock()
	defer t.mu.Unlock()
	at := t.now().UTC()
	t.snap.State, t.snap.LastSuccessAt, t.snap.LastErrorCode = Verified, &at, ""
}

// Failure records a failed call with a short machine code.
func (t *Tracker) Failure(code string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	at := t.now().UTC()
	t.snap.State, t.snap.LastFailureAt, t.snap.LastErrorCode = Failing, &at, SafeCode(code)
}

// Status implements Reporter.
func (t *Tracker) Status() Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.snap
}

// SafeCode reduces s to at most 40 characters of [a-z0-9_] so provider-controlled text cannot reach health output.
func SafeCode(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s) && len(out) < 40; i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_':
			out = append(out, c)
		case c >= 'A' && c <= 'Z':
			out = append(out, c+'a'-'A')
		}
	}
	if len(out) == 0 {
		return "error"
	}
	return string(out)
}
