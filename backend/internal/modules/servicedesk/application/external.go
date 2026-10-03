package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/externalrefs"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
)

const (
	// ExternalSystem is the key of the Autotask integration in external references.
	ExternalSystem = "autotask"
	// ExternalEntity is the entity type of tickets in external references.
	ExternalEntity = "ticket"
	// PushJobType is the job that pushes a ticket to the external service.
	PushJobType    = "servicedesk.external.push"
	PushJobTimeout = 60 * time.Second
)

// TicketPayload is what the external service may know about a ticket: the public parts only.
// Internal comments, the queue and device details beyond the product are never sent.
type TicketPayload struct {
	Reference   string
	Title       string
	Description string
	Status      string
	Priority    string
	Resolution  string
}

// GatewayError is returned by a TicketGateway; Permanent errors are not retried.
type GatewayError struct {
	Message   string
	Permanent bool
}

func (e *GatewayError) Error() string { return e.Message }

// ErrSyncDisabled means the external synchronization is not enabled.
var ErrSyncDisabled = errors.New("servicedesk: external synchronization is not enabled")

// TicketGateway is the outbound port to the external service desk (the Autotask adapter).
type TicketGateway interface {
	// Push creates the external ticket (externalID empty) or updates it, and returns its id.
	Push(ctx context.Context, t TicketPayload, externalID string) (string, error)
}

// InboundEvent is a change reported by the external service (a webhook, once the HTTP
// adapter exists). EventID makes replays harmless.
type InboundEvent struct {
	EventID    string
	ExternalID string
	// Status is the external status mapped by the adapter: "resolved" or "closed" resolve the ticket.
	Status     string
	OccurredAt time.Time
}

// ExternalSync keeps tickets and their external counterparts in step. Audit actions:
// servicedesk.ticket.external_sync_requested and, for inbound changes, the ticket's own
// lifecycle actions performed by the system actor "autotask".
type ExternalSync struct {
	tickets *Service
	store   Store
	q       externalrefs.Querier
	gw      TicketGateway
	enabled bool
}

// NewExternalSync creates the synchronization. q reads and writes external references (the pool).
func NewExternalSync(tickets *Service, store Store, q externalrefs.Querier, gw TicketGateway, enabled bool) *ExternalSync {
	return &ExternalSync{tickets: tickets, store: store, q: q, gw: gw, enabled: enabled}
}

// OnTicketChange is the outbox consumer of TicketCreated, TicketAssigned and TicketResolved: it marks
// the ticket's external reference pending and enqueues one push job (deduplicated per ticket).
func (s *ExternalSync) OnTicketChange(ctx context.Context, tx pgx.Tx, ev events.OutboxEvent) error {
	if !s.enabled {
		return nil
	}
	var p struct {
		TicketID string `json:"ticketId"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return events.Permanent(fmt.Errorf("decode %s payload: %w", ev.EventType, err))
	}
	return s.request(ctx, tx, p.TicketID)
}

func (s *ExternalSync) request(ctx context.Context, tx pgx.Tx, ticketID string) error {
	ref, err := externalrefs.Ensure(ctx, tx, ExternalSystem, ExternalEntity, ticketID)
	if err != nil {
		return err
	}
	version, err := externalrefs.MarkPending(ctx, tx, ref.ID)
	if err != nil {
		return err
	}
	// One job per reference version: a change that arrives while a push runs gets its own job instead
	// of being merged into the running one. Jobs for older versions find the reference synced and stop.
	_, _, err = jobs.Enqueue(ctx, tx, jobs.EnqueueRequest{Type: PushJobType, Payload: map[string]string{"ticketId": ticketID},
		DedupeKey: fmt.Sprintf("%s:%s:%d", PushJobType, ticketID, version), MaxAttempts: 6})
	return err
}

func payloadOf(t Ticket) TicketPayload {
	p := TicketPayload{Reference: t.Reference, Title: t.Title, Status: t.Status, Priority: t.Priority}
	if t.Description != nil {
		p.Description = *t.Description
	}
	if t.Resolution != nil {
		p.Resolution = *t.Resolution
	}
	return p
}

// HandlePush is the job handler: it pushes the current state of a ticket and records the outcome
// on the external reference. Transient failures are retried by the job runner, permanent ones are not.
func (s *ExternalSync) HandlePush(ctx context.Context, job jobs.Job) error {
	var p struct {
		TicketID string `json:"ticketId"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return jobs.Permanent(fmt.Errorf("decode push job: %w", err))
	}
	if !validUUID(p.TicketID) {
		return jobs.Permanent(errors.New("push job without a valid ticket id"))
	}
	// The reference (and its version) is read before the ticket: a change in between leaves the
	// reference at a newer version, so the push is recognised as stale and repeated.
	ref, err := externalrefs.ForEntity(ctx, s.q, ExternalSystem, ExternalEntity, p.TicketID)
	if errors.Is(err, externalrefs.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if ref.SyncState == "synced" {
		return nil // a job for a newer version already pushed this state
	}
	t, err := s.store.Get(ctx, p.TicketID)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	externalID := ""
	if ref.ExternalID != nil {
		externalID = *ref.ExternalID
	}
	got, perr := s.gw.Push(ctx, payloadOf(t), externalID)
	if perr != nil {
		reason := perr.Error()
		if mErr := externalrefs.MarkFailed(ctx, s.q, ref.ID, reason); mErr != nil {
			return mErr
		}
		var ge *GatewayError
		if errors.As(perr, &ge) && ge.Permanent {
			return jobs.Permanent(perr)
		}
		return perr
	}
	if err := externalrefs.MarkSynced(ctx, s.q, ref.ID, got, ref.Version); err != nil {
		if errors.Is(err, externalrefs.ErrExternalIDTaken) {
			_ = externalrefs.MarkFailed(ctx, s.q, ref.ID, err.Error())
			return jobs.Permanent(err)
		}
		return err // ErrStale: the ticket changed during the push; the retry pushes the newer state
	}
	return nil
}

// ApplyInbound applies a change reported by the external service. Replays and events for unknown
// external records change nothing. Events are not ordered against local changes: an older "resolved"
// event that is delivered for the first time after a reopen still resolves the ticket.
func (s *ExternalSync) ApplyInbound(ctx context.Context, ev InboundEvent) error {
	if !s.enabled {
		return ErrSyncDisabled
	}
	if strings.TrimSpace(ev.EventID) == "" || strings.TrimSpace(ev.ExternalID) == "" {
		return invalid("an inbound event needs an event id and an external id")
	}
	ref, err := externalrefs.ByExternalID(ctx, s.q, ExternalSystem, ExternalEntity, ev.ExternalID)
	if errors.Is(err, externalrefs.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	// The event is claimed first: a replay (also after the reporter reopened the ticket) changes nothing.
	// A failure below releases the claim so that the redelivery is applied.
	fresh, err := externalrefs.ClaimEvent(ctx, s.q, ExternalSystem, ev.EventID)
	if err != nil {
		return err
	}
	if !fresh {
		return nil
	}
	now := time.Now().UTC()
	at := ev.OccurredAt
	if at.IsZero() || at.After(now) {
		at = now
	}
	if err := externalrefs.TouchExternal(ctx, s.q, ref.ID, at); err != nil {
		_ = externalrefs.ReleaseEvent(ctx, s.q, ExternalSystem, ev.EventID)
		return err
	}
	if ev.Status == "resolved" || ev.Status == "closed" {
		c := Caller{Actor: audit.SystemActor("autotask"), CorrelationID: "autotask:" + ev.EventID}
		_, err := s.tickets.Transition(ctx, c, Principal{Manage: true}, ref.EntityID, nil, OpResolve, Params{Reason: "Resolved in the external service desk."})
		var tr *InvalidTransitionError
		if err != nil && !errors.As(err, &tr) {
			_ = externalrefs.ReleaseEvent(ctx, s.q, ExternalSystem, ev.EventID)
			return err
		}
	}
	return nil
}

// ExternalState is the synchronization state shown to staff.
type ExternalState struct {
	Enabled           bool
	SyncState         string
	ExternalID        *string
	LastError         *string
	LastSyncedAt      *time.Time
	ExternalUpdatedAt *time.Time
	Attempts          int
}

// StateOf returns the synchronization state of a ticket (staff only; no state when never synced).
func (s *ExternalSync) StateOf(ctx context.Context, p Principal, ticketID string) (ExternalState, error) {
	if !p.staff() || !validUUID(ticketID) {
		return ExternalState{}, ErrNotFound
	}
	st := ExternalState{Enabled: s.enabled}
	ref, err := externalrefs.ForEntity(ctx, s.q, ExternalSystem, ExternalEntity, ticketID)
	if errors.Is(err, externalrefs.ErrNotFound) {
		return st, nil
	}
	if err != nil {
		return ExternalState{}, err
	}
	st.SyncState, st.ExternalID, st.LastError, st.LastSyncedAt, st.ExternalUpdatedAt, st.Attempts =
		ref.SyncState, ref.ExternalID, ref.LastError, ref.LastSyncedAt, ref.ExternalUpdatedAt, ref.Attempts
	return st, nil
}

// Retry asks for another push of a ticket. Requires tickets.manage.
func (s *ExternalSync) Retry(ctx context.Context, c Caller, p Principal, ticketID string) error {
	if err := c.validate(); err != nil {
		return err
	}
	if !p.Manage {
		return ErrForbidden
	}
	if !s.enabled {
		return ErrSyncDisabled
	}
	return s.store.InTx(ctx, func(tx pgx.Tx) error {
		t, err := s.store.LockTx(ctx, tx, ticketID)
		if err != nil {
			return err
		}
		if err := s.request(ctx, tx, t.ID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Change{Action: "servicedesk.ticket.external_sync_requested", TargetType: "ticket", TargetID: t.ID,
			Actor: c.Actor, CorrelationID: c.CorrelationID})
	})
}

func validUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if r != '-' {
				return false
			}
		case !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F'):
			return false
		}
	}
	return true
}
