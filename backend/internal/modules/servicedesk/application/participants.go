package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
)

// Ticket participants (F14 slice A-G, ADR-0034 point 4, design section 6). A participant is an external account a
// Ticket was explicitly shared with. It is the only way an external account sees a Ticket: the grant evaluators
// (Queue grants through Teams or roles) exclude external accounts entirely. A participant sees the Ticket and its
// public comments, never internal comments, Queue, routing, assignee, linked records or other Tickets. Level
// "comment" also allows public comments; "read" only reads. Audit actions: servicedesk.ticket.participant_added and
// servicedesk.ticket.participant_removed (user id, level and expiry only).

// Participant levels.
const (
	LevelRead    = "read"
	LevelComment = "comment"

	// MaxParticipants bounds the participants of one Ticket.
	MaxParticipants = 20
	// MaxParticipantDays bounds the optional expiry of a participation.
	MaxParticipantDays = 365
)

// Errors of the participant operations.
var (
	// ErrParticipantInvalid: the user is not an active external account.
	ErrParticipantInvalid = errors.New("servicedesk: the participant must be an active external account")
	// ErrParticipantLimit: the Ticket already has MaxParticipants participants.
	ErrParticipantLimit = errors.New("servicedesk: this ticket has reached the participant limit")
)

// Participant is an external account a Ticket is shared with.
type Participant struct {
	TicketID    string
	UserID      string
	DisplayName string
	PartyName   string
	Level       string
	AddedBy     string
	AddedAt     time.Time
	ExpiresAt   *time.Time
}

// ParticipantStore is the optional persistence port of participants; a store without it supports none (external
// accounts then see no Ticket at all).
type ParticipantStore interface {
	// ParticipantLevel returns the level of the user on the Ticket; expired participations do not count.
	ParticipantLevel(ctx context.Context, ticketID, userID string) (level string, found bool, err error)
	// UpsertParticipantTx adds or changes a participation and reports whether the row was new. It refuses a new row
	// beyond MaxParticipants with ErrParticipantLimit.
	UpsertParticipantTx(ctx context.Context, tx pgx.Tx, p Participant) (created bool, err error)
	// DeleteParticipantTx removes a participation and reports whether it existed.
	DeleteParticipantTx(ctx context.Context, tx pgx.Tx, ticketID, userID string) (bool, error)
	// Participants lists the unexpired and expired participations of the Ticket, oldest first.
	Participants(ctx context.Context, ticketID string) ([]Participant, error)
}

// ExternalAccount names an active external account and its party.
type ExternalAccount struct {
	UserID      string
	DisplayName string
	PartyName   string
}

// ExternalDirectory is the optional directory capability that knows external accounts.
type ExternalDirectory interface {
	// ActiveExternals returns id -> true for each id that is an active external account with access.
	ActiveExternals(ctx context.Context, ids []string) (map[string]bool, error)
	// SearchExternals finds active external accounts by name (2 or more characters).
	SearchExternals(ctx context.Context, text string, limit int) ([]ExternalAccount, error)
}

// WithExternalAccounts enables participants: ps stores them, ed answers who is an active external account.
func (s *Service) WithExternalAccounts(ps ParticipantStore, ed ExternalDirectory) *Service {
	cp := *s
	cp.participants, cp.externals = ps, ed
	return &cp
}

// participantsEnabled reports whether participants are configured.
func (s *Service) participantsEnabled() bool { return s.participants != nil && s.externals != nil }

// getAsParticipant is Get for an external account: the Ticket only when it is shared with the account, public
// comments only, and none of the routing detail.
func (s *Service) getAsParticipant(ctx context.Context, p Principal, id string) (Detail, error) {
	if s.participants == nil {
		return Detail{}, ErrNotFound
	}
	level, found, err := s.participants.ParticipantLevel(ctx, id, p.UserID)
	if err != nil {
		return Detail{}, err
	}
	if !found {
		return Detail{}, ErrNotFound
	}
	t, err := s.store.Get(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	comments, err := s.store.Comments(ctx, id, false)
	if err != nil {
		return Detail{}, err
	}
	// The neutral view of a Ticket for somebody with no Queue access: the reference the Ticket was raised with and
	// no Queue (shape), and none of the routing and linking fields.
	if err := s.shape(ctx, access{}, &t); err != nil {
		return Detail{}, err
	}
	t = externalView(t)
	open := t.Status != StatusClosed && t.Status != StatusCancelled
	d := Detail{Ticket: t, Comments: comments, Allowed: []string{}, Abilities: Abilities{Comment: level == LevelComment && open}}
	ids := make([]string, 0, len(comments))
	for _, c := range comments {
		ids = append(ids, c.AuthorID)
	}
	if d.Names, err = s.dir.UserNames(ctx, ids); err != nil {
		return Detail{}, fmt.Errorf("load names: %w", err)
	}
	return d, nil
}

// externalView keeps what a participant may read of a Ticket.
func externalView(t Ticket) Ticket {
	return Ticket{ID: t.ID, Reference: t.Reference, Title: t.Title, Description: t.Description, Status: t.Status,
		WaitingReason: t.WaitingReason, WaitingUntil: t.WaitingUntil, Resolution: t.Resolution, Priority: t.Priority, ResolvedAt: t.ResolvedAt, ClosedAt: t.ClosedAt,
		Version: t.Version, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt}
}

// commentAsParticipant adds a public comment for an external account whose participation allows it.
func (s *Service) commentAsParticipant(ctx context.Context, c Caller, p Principal, id, body string) (Comment, error) {
	if s.participants == nil {
		return Comment{}, ErrNotFound
	}
	b, err := cleanText(body, maxText, true, "comment")
	if err != nil {
		return Comment{}, err
	}
	var out Comment
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockTx(ctx, tx, id)
		if err != nil {
			return err
		}
		// Read the participation after the Ticket lock, in the same transaction as the insert.
		level, found, err := s.participants.ParticipantLevel(ctx, cur.ID, p.UserID)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		if level != LevelComment {
			return ErrForbidden
		}
		if cur.Status == StatusCancelled || cur.Status == StatusClosed {
			return &InvalidTransitionError{Operation: "comment", From: cur.Status}
		}
		out, err = s.store.InsertCommentTx(ctx, tx, Comment{TicketID: cur.ID, AuthorID: p.UserID, Body: b, Internal: false})
		if err != nil {
			return err
		}
		if err := audit.Record(ctx, tx, audit.Change{Action: "servicedesk.ticket.comment_added", TargetType: "ticket", TargetID: cur.ID, Actor: c.Actor,
			CorrelationID: c.CorrelationID, Metadata: map[string]any{"commentId": out.ID, "internal": false, "mentionCount": 0, "participant": true}}); err != nil {
			return err
		}
		return publish(ctx, tx, c, "TicketCommentAdded", map[string]any{"ticketId": cur.ID, "commentId": out.ID, "internal": false, "authorId": p.UserID, "mentionedUserIds": []string{}})
	})
	return out, err
}

// AddParticipant shares the Ticket with an external account (or changes level and expiry of an existing share).
// The caller needs work access in the Ticket's Queue.
func (s *Service) AddParticipant(ctx context.Context, c Caller, p Principal, ticketID, userID, level string, expiresAt *time.Time) (Participant, error) {
	if err := c.validate(); err != nil {
		return Participant{}, err
	}
	if !s.participantsEnabled() {
		return Participant{}, ErrForbidden
	}
	if level != LevelRead && level != LevelComment {
		return Participant{}, invalid("level must be read or comment")
	}
	userID = strings.ToLower(userID)
	if !validUUID(userID) {
		return Participant{}, invalid("userId must be a user id")
	}
	if expiresAt != nil {
		at := expiresAt.UTC().Truncate(time.Microsecond)
		now := time.Now().UTC()
		if !at.After(now) || at.After(now.AddDate(0, 0, MaxParticipantDays)) {
			return Participant{}, invalid("expiresAt must be in the future and at most %d days away", MaxParticipantDays)
		}
		expiresAt = &at
	}
	m, err := s.memberships(ctx, p.UserID)
	if err != nil {
		return Participant{}, err
	}
	var out Participant
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, _, ep, err := s.work(ctx, tx, p, m, ticketID)
		if err != nil {
			return err
		}
		if !ep.Manage {
			return ErrForbidden
		}
		if cur.Status == StatusCancelled || cur.Status == StatusClosed {
			return &InvalidTransitionError{Operation: "share", From: cur.Status}
		}
		active, err := s.externals.ActiveExternals(ctx, []string{userID})
		if err != nil {
			return fmt.Errorf("check participant: %w", err)
		}
		if !active[userID] {
			return ErrParticipantInvalid
		}
		out = Participant{TicketID: cur.ID, UserID: userID, Level: level, AddedBy: p.UserID, ExpiresAt: expiresAt}
		created, err := s.participants.UpsertParticipantTx(ctx, tx, out)
		if err != nil {
			return err
		}
		meta := map[string]any{"userId": userID, "level": level, "changed": !created}
		if expiresAt != nil {
			meta["expiresAt"] = expiresAt.UTC().Format(time.RFC3339)
		}
		return audit.Record(ctx, tx, audit.Change{Action: "servicedesk.ticket.participant_added", TargetType: "ticket", TargetID: cur.ID,
			Actor: c.Actor, CorrelationID: c.CorrelationID, Metadata: meta})
	})
	if err != nil {
		return Participant{}, err
	}
	names, err := s.dir.UserNames(ctx, []string{userID})
	if err != nil {
		return Participant{}, fmt.Errorf("load names: %w", err)
	}
	out.DisplayName = names[userID]
	return out, nil
}

// RemoveParticipant ends the sharing of the Ticket with an external account. Removing a share that does not exist
// is ErrNotFound.
func (s *Service) RemoveParticipant(ctx context.Context, c Caller, p Principal, ticketID, userID string) error {
	if err := c.validate(); err != nil {
		return err
	}
	if !s.participantsEnabled() {
		return ErrForbidden
	}
	userID = strings.ToLower(userID)
	if !validUUID(userID) {
		return ErrNotFound
	}
	m, err := s.memberships(ctx, p.UserID)
	if err != nil {
		return err
	}
	return s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, _, ep, err := s.work(ctx, tx, p, m, ticketID)
		if err != nil {
			return err
		}
		if !ep.Manage {
			return ErrForbidden
		}
		removed, err := s.participants.DeleteParticipantTx(ctx, tx, cur.ID, userID)
		if err != nil {
			return err
		}
		if !removed {
			return ErrNotFound
		}
		return audit.Record(ctx, tx, audit.Change{Action: "servicedesk.ticket.participant_removed", TargetType: "ticket", TargetID: cur.ID,
			Actor: c.Actor, CorrelationID: c.CorrelationID, Metadata: map[string]any{"userId": userID}})
	})
}

// Participants lists who a Ticket is shared with. Callers with view access in the Ticket's Queue read it.
func (s *Service) Participants(ctx context.Context, p Principal, ticketID string) ([]Participant, error) {
	if !s.participantsEnabled() {
		return []Participant{}, nil
	}
	a, err := s.resolve(ctx, p)
	if err != nil {
		return nil, err
	}
	t, err := s.store.Get(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	if !a.effT(p, t).staff() {
		return nil, ErrNotFound
	}
	list, err := s.participants.Participants(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(list))
	for i, x := range list {
		ids[i] = x.UserID
	}
	names, err := s.dir.UserNames(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("load names: %w", err)
	}
	for i := range list {
		list[i].DisplayName = names[list[i].UserID]
	}
	return list, nil
}

// ParticipantCandidates finds active external accounts to share a Ticket with. Callers with work access in the
// Ticket's Queue search; the result holds names and party names only.
func (s *Service) ParticipantCandidates(ctx context.Context, p Principal, ticketID, text string) ([]ExternalAccount, error) {
	if !s.participantsEnabled() {
		return []ExternalAccount{}, nil
	}
	a, err := s.resolve(ctx, p)
	if err != nil {
		return nil, err
	}
	t, err := s.store.Get(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	if !a.workT(t) {
		return nil, ErrNotFound
	}
	out, err := s.externals.SearchExternals(ctx, text, 20)
	if err != nil {
		return nil, fmt.Errorf("search external accounts: %w", err)
	}
	return slices.Clone(out), nil
}
