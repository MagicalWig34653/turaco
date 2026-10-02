package application

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxTitleLength       = 200
	maxDescriptionLength = 10000
	maxReasonLength      = 500
)

// Service performs Task operations. Authorization decisions use the
// Principal; the permission check of the route only decides who may reach
// the service at all.
type Service struct {
	store Store
	dir   Directory
	now   func() time.Time
}

// NewService creates a Service. now may be nil.
func NewService(store Store, dir Directory, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, dir: dir, now: now}
}

// ---- access ----

// access carries what the caller may see and do, resolved once per request.
type access struct {
	p     Principal
	teams map[string]struct{} // the caller's current active Teams
}

func (s *Service) access(ctx context.Context, p Principal) (access, error) {
	ids, err := s.dir.CurrentTeamIDs(ctx, p.UserID)
	if err != nil {
		return access{}, fmt.Errorf("load teams of caller: %w", err)
	}
	a := access{p: p, teams: make(map[string]struct{}, len(ids))}
	for _, id := range ids {
		a.teams[id] = struct{}{}
	}
	return a, nil
}

func (a access) assignedToCaller(t Task) bool {
	if t.AssignedUserID != nil && *t.AssignedUserID == a.p.UserID {
		return true
	}
	if t.AssignedTeamID != nil {
		_, ok := a.teams[*t.AssignedTeamID]
		return ok
	}
	return false
}

func (a access) canSee(t Task) bool {
	return a.p.ViewAll || a.p.Manage || (a.p.Work && a.assignedToCaller(t))
}

func (a access) canWork(t Task) bool {
	return a.p.Manage || (a.p.Work && a.assignedToCaller(t))
}

// ---- validation ----

func cleanTitle(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > maxTitleLength || !utf8.ValidString(s) {
		return "", invalid("title must be 1-%d characters", maxTitleLength)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", invalid("title must not contain control characters")
		}
	}
	return s, nil
}

// cleanDescription trims; an empty description is stored as absent. Line
// breaks and tabs are allowed.
func cleanDescription(s string) (*string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(s) > maxDescriptionLength || !utf8.ValidString(s) {
		return nil, invalid("description must be at most %d characters", maxDescriptionLength)
	}
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return nil, invalid("description must not contain control characters")
		}
	}
	return &s, nil
}

func cleanReason(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > maxReasonLength || !utf8.ValidString(s) {
		return "", invalid("reason must be 1-%d characters", maxReasonLength)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", invalid("reason must not contain control characters")
		}
	}
	return s, nil
}

func validPriority(p string) error {
	if !contains(priorities, p) {
		return invalid("priority must be one of low, normal, high, urgent")
	}
	return nil
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC().Truncate(time.Microsecond)
	return &u
}

// checkAssignees verifies that the referenced User and Team are active.
func (s *Service) checkAssignees(ctx context.Context, userID, teamID *string) error {
	if userID != nil {
		active, err := s.dir.ActiveUsers(ctx, []string{*userID})
		if err != nil {
			return fmt.Errorf("check assignee: %w", err)
		}
		if !active[*userID] {
			return ErrAssigneeInvalid
		}
	}
	if teamID != nil {
		active, err := s.dir.ActiveTeams(ctx, []string{*teamID})
		if err != nil {
			return fmt.Errorf("check assignee: %w", err)
		}
		if !active[*teamID] {
			return ErrAssigneeInvalid
		}
	}
	return nil
}

// ---- reads ----

// view adds assignee display names.
func (s *Service) view(ctx context.Context, tasks []Task) ([]TaskView, error) {
	var userIDs, teamIDs []string
	for _, t := range tasks {
		if t.AssignedUserID != nil {
			userIDs = append(userIDs, *t.AssignedUserID)
		}
		if t.AssignedTeamID != nil {
			teamIDs = append(teamIDs, *t.AssignedTeamID)
		}
	}
	userNames, err := s.dir.UserNames(ctx, userIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve user names: %w", err)
	}
	teamNames, err := s.dir.TeamNames(ctx, teamIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve team names: %w", err)
	}
	out := make([]TaskView, 0, len(tasks))
	for _, t := range tasks {
		v := TaskView{Task: t}
		if t.AssignedUserID != nil {
			if n, ok := userNames[*t.AssignedUserID]; ok {
				v.AssignedUserName = &n
			}
		}
		if t.AssignedTeamID != nil {
			if n, ok := teamNames[*t.AssignedTeamID]; ok {
				v.AssignedTeamName = &n
			}
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Service) viewOne(ctx context.Context, t Task) (TaskView, error) {
	vs, err := s.view(ctx, []Task{t})
	if err != nil {
		return TaskView{}, err
	}
	return vs[0], nil
}

// Get returns a task the caller may see; others are ErrNotFound.
func (s *Service) Get(ctx context.Context, p Principal, id string) (TaskView, error) {
	a, err := s.access(ctx, p)
	if err != nil {
		return TaskView{}, err
	}
	t, err := s.store.Get(ctx, id)
	if err != nil {
		return TaskView{}, err
	}
	if !a.canSee(t) {
		return TaskView{}, ErrNotFound
	}
	return s.viewOne(ctx, t)
}

// ListFilter is the caller-selectable part of a list.
type ListFilter struct {
	Statuses       []string
	Priority       string
	AssignedUserID string
	AssignedTeamID string
	Overdue        bool
	TitlePrefix    string
	// Mine restricts a caller that may see all tasks to its own work; callers
	// that may only work their own tasks always get that restriction.
	Mine bool
	Page Page
}

func (f ListFilter) validate() error {
	for _, st := range f.Statuses {
		if !contains(statuses, st) {
			return invalid("status must be one of open, in_progress, blocked, completed, cancelled")
		}
	}
	if f.Priority != "" {
		return validPriority(f.Priority)
	}
	return nil
}

// List returns tasks in the shared order. Callers that only hold tasks.work
// see only tasks assigned to them or their Teams.
func (s *Service) List(ctx context.Context, p Principal, f ListFilter) (Result[TaskView], error) {
	if err := f.validate(); err != nil {
		return Result[TaskView]{}, err
	}
	a, err := s.access(ctx, p)
	if err != nil {
		return Result[TaskView]{}, err
	}
	q := ListQuery{
		Statuses: f.Statuses, Priority: f.Priority, AssignedUserID: f.AssignedUserID,
		AssignedTeamID: f.AssignedTeamID, Overdue: f.Overdue, TitlePrefix: f.TitlePrefix, Page: f.Page.Normalize(),
	}
	if f.Mine || !(p.ViewAll || p.Manage) {
		if !p.Work && !p.ViewAll && !p.Manage {
			return Result[TaskView]{Items: []TaskView{}}, nil
		}
		m := a.mine()
		q.Mine = &m
	}
	return s.list(ctx, q)
}

// MyWork returns the caller's unfinished tasks (assigned to the caller or one
// of its Teams) in the shared order.
func (s *Service) MyWork(ctx context.Context, p Principal, page Page) (Result[TaskView], error) {
	a, err := s.access(ctx, p)
	if err != nil {
		return Result[TaskView]{}, err
	}
	m := a.mine()
	return s.list(ctx, ListQuery{
		Statuses: []string{StatusOpen, StatusInProgress, StatusBlocked}, Mine: &m, Page: page.Normalize(),
	})
}

func (a access) mine() Mine {
	m := Mine{UserID: a.p.UserID, TeamIDs: make([]string, 0, len(a.teams))}
	for id := range a.teams {
		m.TeamIDs = append(m.TeamIDs, id)
	}
	sort.Strings(m.TeamIDs)
	return m
}

func (s *Service) list(ctx context.Context, q ListQuery) (Result[TaskView], error) {
	res, err := s.store.List(ctx, q)
	if err != nil {
		return Result[TaskView]{}, err
	}
	views, err := s.view(ctx, res.Items)
	if err != nil {
		return Result[TaskView]{}, err
	}
	return Result[TaskView]{Items: views, NextCursor: res.NextCursor}, nil
}
