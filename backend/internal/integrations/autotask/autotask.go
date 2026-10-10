// Package autotask is the adapter to the Autotask service desk (docs/integrations/autotask.md).
//
// It holds the Gateway contract the Service Desk module is wired to, an in-memory Fake for tests and local
// development, the "not configured" placeholder, and the REST client (rest.go), which is written strictly from the
// vendor documentation and unverified against a live Autotask database. Webhooks and polling for inbound changes
// are not implemented.
package autotask

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Ticket is what the adapter sends to Autotask (public ticket data only).
type Ticket struct {
	Reference   string
	Title       string
	Description string
	Status      string
	Priority    string
	Resolution  string
}

// ErrNotConfigured is returned while the Autotask API user is not configured (AUTOTASK_*).
var ErrNotConfigured = errors.New("autotask: the REST client is not configured")

// Gateway creates or updates Autotask tickets.
type Gateway interface {
	// Upsert creates the Autotask ticket (externalID empty) or updates it and returns its id.
	// A permanent error (rejected data) is reported as *Error with Permanent set.
	Upsert(ctx context.Context, t Ticket, externalID string) (string, error)
}

// Error is a failure reported by Autotask or the connection.
type Error struct {
	Message   string
	Permanent bool
}

func (e *Error) Error() string { return e.Message }

// NotConfigured is the placeholder gateway: every call fails permanently with ErrNotConfigured,
// which the Service Desk shows as a failed synchronization.
type NotConfigured struct{}

func (NotConfigured) Upsert(context.Context, Ticket, string) (string, error) {
	return "", &Error{Message: ErrNotConfigured.Error(), Permanent: true}
}

// Fake is an in-memory Autotask for tests and local development.
type Fake struct {
	mu      sync.Mutex
	next    int
	Tickets map[string]Ticket
	// Fail, when set, makes the next calls fail with it.
	Fail error
	// Calls counts Upsert calls.
	Calls int
}

func NewFake() *Fake { return &Fake{Tickets: map[string]Ticket{}} }

func (f *Fake) Upsert(_ context.Context, t Ticket, externalID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls++
	if f.Fail != nil {
		return "", f.Fail
	}
	if externalID == "" {
		f.next++
		externalID = fmt.Sprintf("T2026%04d.%04d", 1, f.next)
	}
	f.Tickets[externalID] = t
	return externalID, nil
}
