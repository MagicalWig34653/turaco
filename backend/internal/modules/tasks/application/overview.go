package application

import (
	"context"
	"fmt"
	"time"
)

// Manager overview of Tasks (simulation backlog, ADR-0033): the open Tasks grouped by owner, with the overdue and
// the ownerless ones called out. It is a read-only breakdown the filter engine cannot express, not a reporting
// system; every number maps to a filter over the tasks catalog (status, due_at, assigned_user, assigned_team).
//
// Only callers who see every Task (tasks.view or tasks.manage) may read it. A caller who works only the Tasks
// assigned to them or their Teams has no management view of other people's work, so this answers ErrForbidden
// for them exactly as a read of a list they may not use would.

// Overview limits keep the response bounded regardless of the data.
const (
	OverviewMaxOwners = 50
	OverviewMaxOldest = 10
)

// OverviewQuery is the store's input.
type OverviewQuery struct {
	// Now splits overdue from not yet due.
	Now         time.Time
	OwnerLimit  int
	OldestLimit int
}

// OverviewOwner is the open and overdue count of one owner (a User or a Team).
type OverviewOwner struct {
	ID      string
	Open    int
	Overdue int
}

// OverviewTotals count the open Tasks (open, in progress, blocked).
type OverviewTotals struct {
	Open    int
	Overdue int
	Blocked int
	// Unowned are open Tasks assigned to neither a User nor a Team.
	Unowned int
}

// OverviewData is what the store reads.
type OverviewData struct {
	Totals         OverviewTotals
	Users          []OverviewOwner
	UsersTruncated bool
	Teams          []OverviewOwner
	TeamsTruncated bool
	OldestOverdue  []Task
	OldestUnowned  []Task
}

// OverviewStore is the persistence port of the manager overview. The repository implements it next to Store.
type OverviewStore interface {
	TaskOverview(ctx context.Context, q OverviewQuery) (OverviewData, error)
}

// OverviewOwnerView is an owner row with the display name.
type OverviewOwnerView struct {
	OverviewOwner
	Name string
}

// Overview is the manager overview of Tasks for one caller.
type Overview struct {
	GeneratedAt    time.Time
	Totals         OverviewTotals
	Users          []OverviewOwnerView
	UsersTruncated bool
	Teams          []OverviewOwnerView
	TeamsTruncated bool
	OldestOverdue  []TaskView
	OldestUnowned  []TaskView
}

// Overview returns the manager overview of Tasks.
func (s *Service) Overview(ctx context.Context, p Principal) (Overview, error) {
	if p.UserID == "" || !(p.ViewAll || p.Manage) {
		return Overview{}, ErrForbidden
	}
	os, ok := s.store.(OverviewStore)
	if !ok {
		return Overview{}, fmt.Errorf("tasks: store does not support the overview")
	}
	now := s.now().UTC()
	data, err := os.TaskOverview(ctx, OverviewQuery{Now: now, OwnerLimit: OverviewMaxOwners, OldestLimit: OverviewMaxOldest})
	if err != nil {
		return Overview{}, fmt.Errorf("task overview: %w", err)
	}
	userIDs, teamIDs := []string{}, []string{}
	for _, o := range data.Users {
		userIDs = append(userIDs, o.ID)
	}
	for _, o := range data.Teams {
		teamIDs = append(teamIDs, o.ID)
	}
	userNames, err := s.dir.UserNames(ctx, userIDs)
	if err != nil {
		return Overview{}, fmt.Errorf("resolve user names: %w", err)
	}
	teamNames, err := s.dir.TeamNames(ctx, teamIDs)
	if err != nil {
		return Overview{}, fmt.Errorf("resolve team names: %w", err)
	}
	out := Overview{GeneratedAt: now, Totals: data.Totals, UsersTruncated: data.UsersTruncated, TeamsTruncated: data.TeamsTruncated}
	for _, o := range data.Users {
		out.Users = append(out.Users, OverviewOwnerView{OverviewOwner: o, Name: userNames[o.ID]})
	}
	for _, o := range data.Teams {
		out.Teams = append(out.Teams, OverviewOwnerView{OverviewOwner: o, Name: teamNames[o.ID]})
	}
	if out.OldestOverdue, err = s.view(ctx, data.OldestOverdue); err != nil {
		return Overview{}, err
	}
	if out.OldestUnowned, err = s.view(ctx, data.OldestUnowned); err != nil {
		return Overview{}, err
	}
	return out, nil
}
