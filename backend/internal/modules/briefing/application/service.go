package application

import (
	"context"
	"time"
)

// Service performs briefing operations.
//
// Audit actions: briefing.item.created, .updated, .published, .withdrawn,
// .deleted. Audit states carry status, severity, validity and version; titles
// and bodies are never copied into the audit log.
type Service struct {
	store     Store
	now       func() time.Time
	incidents IncidentAnnouncements
}

// NewService creates a Service. now may be nil.
func NewService(store Store, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, now: now}
}

func (s *Service) canSee(p Principal, it Item) bool {
	if p.Manage {
		return true
	}
	return p.View && it.Status == StatusPublished && (it.ValidUntil == nil || it.ValidUntil.After(s.now()))
}

// Get returns an item the caller may see; others are ErrNotFound.
func (s *Service) Get(ctx context.Context, p Principal, id string) (Item, error) {
	it, err := s.store.Get(ctx, id)
	if err != nil {
		return Item{}, err
	}
	if !s.canSee(p, it) {
		return Item{}, ErrNotFound
	}
	return it, nil
}

// List returns items newest first. Viewers see published, unexpired items
// only; managers see every item and may filter by status.
func (s *Service) List(ctx context.Context, p Principal, status string, page Page) (Result, error) {
	if !p.View && !p.Manage {
		return Result{Items: []Item{}}, nil
	}
	if status != "" && !contains(statuses, status) {
		return Result{}, invalid("status must be one of draft, published, withdrawn")
	}
	q := ListQuery{Page: page.Normalize()}
	if p.Manage {
		q.Status = status
	} else {
		q.PublishedOnly = true
	}
	return s.store.List(ctx, q)
}

// CreateInput is the input of Create.
type CreateInput struct {
	Title      string
	Body       string
	Severity   string // empty means info
	Audience   string // empty means it
	ValidUntil *time.Time
}

// Create creates a draft. Requires briefing.manage.
func (s *Service) Create(ctx context.Context, c Caller, p Principal, in CreateInput) (Item, error) {
	if err := c.validate(); err != nil {
		return Item{}, err
	}
	if !p.Manage {
		return Item{}, ErrForbidden
	}
	title, err := cleanTitle(in.Title)
	if err != nil {
		return Item{}, err
	}
	body, err := cleanBody(in.Body)
	if err != nil {
		return Item{}, err
	}
	severity := in.Severity
	if severity == "" {
		severity = SeverityInfo
	}
	if err := validSeverity(severity); err != nil {
		return Item{}, err
	}
	audience := in.Audience
	if audience == "" {
		audience = AudienceIT
	}
	if err := validAudience(audience); err != nil {
		return Item{}, err
	}
	var author *string
	if c.Actor.UserID != "" {
		author = &c.Actor.UserID
	}
	return s.store.Insert(ctx, c, NewItem{Title: title, Body: body, Severity: severity, Audience: audience, ValidUntil: utcPtr(in.ValidUntil), Author: author})
}

// UpdateInput changes a draft; nil fields stay unchanged.
type UpdateInput struct {
	Title           *string
	Body            *string
	Severity        *string
	Audience        *string
	ValidUntil      *time.Time
	ClearValidUntil bool
}

func (in UpdateInput) empty() bool {
	return in.Title == nil && in.Body == nil && in.Severity == nil && in.Audience == nil && in.ValidUntil == nil && !in.ClearValidUntil
}

// Update changes a draft. Published and withdrawn items are immutable: to
// correct a published item, withdraw it and create a new one, so what readers
// saw stays traceable. Requires briefing.manage.
func (s *Service) Update(ctx context.Context, c Caller, p Principal, id string, expected *int, in UpdateInput) (Item, error) {
	if err := c.validate(); err != nil {
		return Item{}, err
	}
	if !p.Manage {
		return Item{}, ErrForbidden
	}
	if in.empty() {
		return Item{}, invalid("at least one field to change is required")
	}
	if in.ClearValidUntil && in.ValidUntil != nil {
		return Item{}, invalid("clearing the expiry excludes setting it")
	}
	var title, body *string
	if in.Title != nil {
		t, err := cleanTitle(*in.Title)
		if err != nil {
			return Item{}, err
		}
		title = &t
	}
	if in.Body != nil {
		b, err := cleanBody(*in.Body)
		if err != nil {
			return Item{}, err
		}
		body = &b
	}
	if in.Severity != nil {
		if err := validSeverity(*in.Severity); err != nil {
			return Item{}, err
		}
	}
	if in.Audience != nil {
		if err := validAudience(*in.Audience); err != nil {
			return Item{}, err
		}
	}
	return s.store.Change(ctx, c, id, func(cur Item) (Change, error) {
		if expected != nil && *expected != cur.Version {
			return Change{}, ErrVersionConflict
		}
		if cur.Status != StatusDraft {
			return Change{}, &InvalidTransitionError{Operation: "update", From: cur.Status}
		}
		next := cur
		var changed []string
		if title != nil && *title != cur.Title {
			next.Title = *title
			changed = append(changed, "title")
		}
		if body != nil && *body != cur.Body {
			next.Body = *body
			changed = append(changed, "body")
		}
		if in.Severity != nil && *in.Severity != cur.Severity {
			next.Severity = *in.Severity
			changed = append(changed, "severity")
		}
		if in.Audience != nil && *in.Audience != cur.Audience {
			next.Audience = *in.Audience
			changed = append(changed, "audience")
		}
		if in.ClearValidUntil && cur.ValidUntil != nil {
			next.ValidUntil = nil
			changed = append(changed, "validUntil")
		} else if in.ValidUntil != nil && !equalTimePtr(utcPtr(in.ValidUntil), cur.ValidUntil) {
			next.ValidUntil = utcPtr(in.ValidUntil)
			changed = append(changed, "validUntil")
		}
		if len(changed) == 0 {
			return Change{NoChange: true, Next: cur}, nil
		}
		return Change{Next: next, Action: "briefing.item.updated", Metadata: map[string]any{"changedFields": changed}}, nil
	})
}

// Publish publishes a draft; it is then visible to everybody with briefing.view
// until it expires or is withdrawn. A draft whose expiry has passed cannot be
// published. Requires briefing.manage. Emits BriefingItemPublished.
func (s *Service) Publish(ctx context.Context, c Caller, p Principal, id string, expected *int) (Item, error) {
	if err := c.validate(); err != nil {
		return Item{}, err
	}
	if !p.Manage {
		return Item{}, ErrForbidden
	}
	now := s.now().UTC().Truncate(time.Microsecond)
	return s.store.Change(ctx, c, id, func(cur Item) (Change, error) {
		if expected != nil && *expected != cur.Version {
			return Change{}, ErrVersionConflict
		}
		if cur.Status != StatusDraft {
			return Change{}, &InvalidTransitionError{Operation: "publish", From: cur.Status}
		}
		if cur.ValidUntil != nil && !cur.ValidUntil.After(now) {
			return Change{}, invalid("the expiry is in the past; change it before publishing")
		}
		next := cur
		next.Status, next.PublishedAt = StatusPublished, &now
		if c.Actor.UserID != "" {
			by := c.Actor.UserID
			next.PublishedByUserID = &by
		}
		return Change{Next: next, Action: "briefing.item.published", Events: []Event{
			{Type: "BriefingItemPublished", Payload: map[string]any{"itemId": cur.ID, "severity": cur.Severity}},
		}}, nil
	})
}

// Withdraw takes a published item back; it stays visible to managers.
// Requires briefing.manage.
func (s *Service) Withdraw(ctx context.Context, c Caller, p Principal, id string, expected *int) (Item, error) {
	if err := c.validate(); err != nil {
		return Item{}, err
	}
	if !p.Manage {
		return Item{}, ErrForbidden
	}
	now := s.now().UTC().Truncate(time.Microsecond)
	return s.store.Change(ctx, c, id, func(cur Item) (Change, error) {
		if expected != nil && *expected != cur.Version {
			return Change{}, ErrVersionConflict
		}
		if cur.Status != StatusPublished {
			return Change{}, &InvalidTransitionError{Operation: "withdraw", From: cur.Status}
		}
		next := cur
		next.Status, next.WithdrawnAt = StatusWithdrawn, &now
		if c.Actor.UserID != "" {
			by := c.Actor.UserID
			next.WithdrawnByUserID = &by
		}
		return Change{Next: next, Action: "briefing.item.withdrawn"}, nil
	})
}

// Delete removes a draft. Published and withdrawn items are kept as history.
// Requires briefing.manage.
func (s *Service) Delete(ctx context.Context, c Caller, p Principal, id string, expected *int) error {
	if err := c.validate(); err != nil {
		return err
	}
	if !p.Manage {
		return ErrForbidden
	}
	return s.store.Delete(ctx, c, id, func(cur Item) error {
		if expected != nil && *expected != cur.Version {
			return ErrVersionConflict
		}
		if cur.Status != StatusDraft {
			return &InvalidTransitionError{Operation: "delete", From: cur.Status}
		}
		return nil
	})
}
