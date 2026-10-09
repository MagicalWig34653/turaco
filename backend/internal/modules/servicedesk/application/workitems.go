package application

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// My Work contribution (ADR-0033 V8): the open Tickets that wait for the caller. Two lists share one
// implementation: Tickets assigned to the caller, and unassigned Tickets routed to one of the caller's Teams.
// Both apply the caller's own authorization to every row: a Ticket is listed only when the caller may still view it
// (the global permissions, a view grant on its Queue, or being its reporter or affected User), so losing access to
// a Queue removes its Tickets from My Work at once.

// WorkSource selects one of the two lists.
type WorkSource int

const (
	// WorkAssigned: open Tickets whose assignee is the caller.
	WorkAssigned WorkSource = iota
	// WorkTeam: open, unassigned Tickets whose routing Team is one of the caller's Teams.
	WorkTeam
)

// WorkQuery is the store's input.
type WorkQuery struct {
	Source  WorkSource
	UserID  string
	TeamIDs []string
	// Global means every Queue is viewable; otherwise QueueIDs lists the viewable ones.
	Global   bool
	QueueIDs []string
	// After* is the keyset position (sort rank, ticket id); AfterID empty starts at the beginning.
	AfterRank int
	AfterID   string
	Limit     int
}

// WorkRow is a Ticket with its shared sort rank (priority, waiting Tickets after active ones).
type WorkRow struct {
	Ticket Ticket
	Rank   int
	Cursor string
}

// WorkStore is the persistence port of the My Work contribution.
type WorkStore interface {
	WorkTickets(ctx context.Context, q WorkQuery) ([]WorkRow, error)
	WorkCount(ctx context.Context, q WorkQuery, limit int) (int, error)
}

type workCursor struct {
	Rank int    `json:"r"`
	ID   string `json:"i"`
}

func encodeWorkCursor(rank int, id string) string {
	raw, _ := json.Marshal(workCursor{Rank: rank, ID: id})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeWorkCursor(s string) (workCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(raw) > 200 {
		return workCursor{}, ErrInvalidCursor
	}
	var c workCursor
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil || c.Rank < 0 || c.Rank > 7 || !uuidPattern.MatchString(c.ID) {
		return workCursor{}, ErrInvalidCursor
	}
	return c, nil
}

// WorkRank is the shared sort rank of a Ticket: priority first, waiting Tickets after the active ones.
func WorkRank(priority, status string) int {
	r := 3
	switch priority {
	case "urgent":
		r = 0
	case "high":
		r = 1
	case "normal":
		r = 2
	}
	if status == StatusWaiting {
		r += 4
	}
	return r
}

func (s *Service) workQuery(ctx context.Context, p Principal, src WorkSource) (WorkQuery, access, error) {
	if p.UserID == "" {
		return WorkQuery{}, access{}, ErrForbidden
	}
	a, err := s.resolve(ctx, p)
	if err != nil {
		return WorkQuery{}, access{}, err
	}
	q := WorkQuery{Source: src, UserID: p.UserID, Global: a.global(), QueueIDs: a.viewIDs()}
	if src == WorkTeam {
		m, err := s.memberships(ctx, p.UserID)
		if err != nil {
			return WorkQuery{}, access{}, err
		}
		q.TeamIDs = m.teams
	}
	return q, a, nil
}

// MyWorkTickets returns one page of the caller's work list in the shared order (rank, id), after the cursor.
func (s *Service) MyWorkTickets(ctx context.Context, p Principal, src WorkSource, cursor string, limit int) ([]WorkRow, error) {
	ws, ok := s.store.(WorkStore)
	if !ok {
		return nil, fmt.Errorf("servicedesk: store does not support the work list")
	}
	q, a, err := s.workQuery(ctx, p, src)
	if err != nil {
		return nil, err
	}
	if src == WorkTeam && len(q.TeamIDs) == 0 {
		return []WorkRow{}, nil
	}
	if cursor != "" {
		c, err := decodeWorkCursor(cursor)
		if err != nil {
			return nil, err
		}
		q.AfterRank, q.AfterID = c.Rank, strings.ToLower(c.ID)
	}
	q.Limit = min(max(limit, 1), MaxLimit)
	rows, err := ws.WorkTickets(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list work tickets: %w", err)
	}
	for i := range rows {
		rows[i].Cursor = encodeWorkCursor(rows[i].Rank, rows[i].Ticket.ID)
	}
	ts := make([]*Ticket, len(rows))
	for i := range rows {
		ts[i] = &rows[i].Ticket
	}
	if err := s.shape(ctx, a, ts...); err != nil {
		return nil, err
	}
	return rows, nil
}

// MyWorkTicketCount counts the list up to limit rows (limit+1 rows are read at most); the caller reports "limit+".
func (s *Service) MyWorkTicketCount(ctx context.Context, p Principal, src WorkSource, limit int) (int, error) {
	ws, ok := s.store.(WorkStore)
	if !ok {
		return 0, fmt.Errorf("servicedesk: store does not support the work list")
	}
	q, _, err := s.workQuery(ctx, p, src)
	if err != nil {
		return 0, err
	}
	if src == WorkTeam && len(q.TeamIDs) == 0 {
		return 0, nil
	}
	return ws.WorkCount(ctx, q, limit)
}
