package application

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// Service performs Knowledge operations. Audit actions: knowledge.article.created,
// .updated, .published, .retired and .republished. Article text is never copied into
// audit; ids, statuses, audience and the names of changed fields are.
type Service struct{ store Store }

func NewService(store Store) *Service { return &Service{store: store} }

func state(a *Article) any {
	if a == nil {
		return nil
	}
	return map[string]any{"status": a.Status, "audience": a.Audience, "version": a.Version}
}

func record(ctx context.Context, tx pgx.Tx, c Caller, action string, before, after *Article, meta map[string]any) error {
	id := ""
	if after != nil {
		id = after.ID
	} else {
		id = before.ID
	}
	if len(meta) == 0 {
		meta = nil
	}
	return audit.Record(ctx, tx, audit.Change{Action: action, TargetType: "knowledge_article", TargetID: id, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Before: state(before), After: state(after), Metadata: meta})
}

func publish(ctx context.Context, tx pgx.Tx, c Caller, a Article) error {
	var actor *string
	if c.Actor.UserID != "" {
		u := c.Actor.UserID
		actor = &u
	}
	return events.Publish(ctx, tx, events.Publication{Type: "KnowledgeArticlePublished", ActorID: actor, CorrelationID: c.CorrelationID,
		Payload: map[string]any{"articleId": a.ID, "audience": a.Audience}})
}

func clean(s string, limit int, required, multiline bool, what string) (string, error) {
	s = strings.TrimSpace(s)
	if (required && s == "") || utf8.RuneCountInString(s) > limit || !utf8.ValidString(s) || safetext.ContainsUnsafe(s, multiline) {
		return "", invalid("%s must be at most %d characters, without control or invisible formatting characters%s", what, limit,
			map[bool]string{true: ", and must not be empty", false: ""}[required])
	}
	return s, nil
}

// Input is the editable content of an article.
type Input struct {
	Title    string
	Summary  string
	Body     string
	Audience string
}

func (in Input) clean() (Input, error) {
	var err error
	if in.Title, err = clean(in.Title, maxTitle, true, false, "title"); err != nil {
		return in, err
	}
	if in.Summary, err = clean(in.Summary, maxSummary, false, false, "summary"); err != nil {
		return in, err
	}
	if in.Body, err = clean(in.Body, maxBody, true, true, "body"); err != nil {
		return in, err
	}
	if in.Audience == "" {
		in.Audience = AudienceInternal
	}
	if in.Audience != AudienceInternal && in.Audience != AudienceEmployee {
		return in, invalid("audience must be internal or employee")
	}
	return in, nil
}

// Create writes a draft. Requires knowledge.manage.
func (s *Service) Create(ctx context.Context, c Caller, p Principal, in Input) (Article, error) {
	if err := c.validate(); err != nil {
		return Article{}, err
	}
	if !p.Manage {
		return Article{}, ErrForbidden
	}
	in, err := in.clean()
	if err != nil {
		return Article{}, err
	}
	a := Article{Title: in.Title, Summary: in.Summary, Body: in.Body, Audience: in.Audience, Status: StatusDraft}
	if c.Actor.UserID != "" {
		u := c.Actor.UserID
		a.AuthorID = &u
	}
	var out Article
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		out, err = s.store.InsertTx(ctx, tx, a)
		if err != nil {
			return err
		}
		return record(ctx, tx, c, "knowledge.article.created", nil, &out, nil)
	})
	return out, err
}

// Update changes the content of an article in any status except retired.
// Requires knowledge.manage; expected must match the current version.
func (s *Service) Update(ctx context.Context, c Caller, p Principal, id string, expected int, in Input) (Article, error) {
	if err := c.validate(); err != nil {
		return Article{}, err
	}
	if !p.Manage {
		return Article{}, ErrForbidden
	}
	in, err := in.clean()
	if err != nil {
		return Article{}, err
	}
	var out Article
	err = s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if cur.Version != expected {
			return ErrVersionConflict
		}
		if cur.Status == StatusRetired {
			return &InvalidTransitionError{Operation: "update", From: cur.Status}
		}
		var changed []string
		next := cur
		for _, f := range []struct {
			name     string
			old, new *string
		}{{"title", &cur.Title, &in.Title}, {"summary", &cur.Summary, &in.Summary}, {"body", &cur.Body, &in.Body}, {"audience", &cur.Audience, &in.Audience}} {
			if *f.old != *f.new {
				changed = append(changed, f.name)
			}
		}
		if len(changed) == 0 {
			out = cur
			return nil
		}
		next.Title, next.Summary, next.Body, next.Audience = in.Title, in.Summary, in.Body, in.Audience
		out, err = s.store.UpdateTx(ctx, tx, next)
		if err != nil {
			return err
		}
		return record(ctx, tx, c, "knowledge.article.updated", &cur, &out, map[string]any{"changedFields": changed})
	})
	return out, err
}

// Publish makes a draft (or republishes a retired article) readable by its audience.
// Requires knowledge.manage.
func (s *Service) Publish(ctx context.Context, c Caller, p Principal, id string, expected *int) (Article, error) {
	return s.move(ctx, c, p, id, expected, "publish", []string{StatusDraft, StatusRetired}, StatusPublished)
}

// Retire withdraws a published article. Requires knowledge.manage.
func (s *Service) Retire(ctx context.Context, c Caller, p Principal, id string, expected *int) (Article, error) {
	return s.move(ctx, c, p, id, expected, "retire", []string{StatusPublished}, StatusRetired)
}

func (s *Service) move(ctx context.Context, c Caller, p Principal, id string, expected *int, op string, from []string, to string) (Article, error) {
	if err := c.validate(); err != nil {
		return Article{}, err
	}
	if !p.Manage {
		return Article{}, ErrForbidden
	}
	var out Article
	err := s.store.InTx(ctx, func(tx pgx.Tx) error {
		cur, err := s.store.LockTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if expected != nil && *expected != cur.Version {
			return ErrVersionConflict
		}
		if !slices.Contains(from, cur.Status) {
			return &InvalidTransitionError{Operation: op, From: cur.Status}
		}
		next := cur
		next.Status = to
		if to == StatusPublished {
			now := time.Now().UTC()
			next.PublishedAt = &now
		}
		out, err = s.store.UpdateTx(ctx, tx, next)
		if err != nil {
			return err
		}
		action := "knowledge.article." + op + "ed"
		if op == "publish" && cur.Status == StatusRetired {
			action = "knowledge.article.republished"
		}
		if err := record(ctx, tx, c, action, &cur, &out, nil); err != nil {
			return err
		}
		if to == StatusPublished {
			return publish(ctx, tx, c, out)
		}
		return nil
	})
	return out, err
}

// visible reports whether the caller may read the article.
func visible(p Principal, a Article) bool {
	switch {
	case p.Manage:
		return true
	case a.Status != StatusPublished:
		return false
	case a.Audience == AudienceEmployee:
		return p.UserID != ""
	}
	return p.View
}

// Get returns an article the caller may read (404 otherwise).
func (s *Service) Get(ctx context.Context, p Principal, id string) (Article, error) {
	a, err := s.store.Get(ctx, id)
	if err != nil {
		return Article{}, err
	}
	if !visible(p, a) {
		return Article{}, ErrNotFound
	}
	return a, nil
}

// List searches or lists articles the caller may read. Text is matched with full-text
// search; status filters further. Drafts and retired articles need knowledge.manage.
func (s *Service) List(ctx context.Context, p Principal, text, status string, page Page) (Result, error) {
	if p.UserID == "" {
		return Result{}, ErrForbidden
	}
	q := Query{Text: strings.TrimSpace(text), Page: page.Normalize()}
	if utf8.RuneCountInString(q.Text) > 200 || !utf8.ValidString(q.Text) || strings.ContainsRune(q.Text, 0) {
		return Result{}, invalid("the search text is invalid")
	}
	if status != "" && !slices.Contains([]string{StatusDraft, StatusPublished, StatusRetired}, status) {
		return Result{}, invalid("unknown status")
	}
	switch {
	case p.Manage:
		q.Audiences = []string{AudienceInternal, AudienceEmployee}
		if status != "" {
			q.Statuses = []string{status}
		}
	default:
		if status != "" && status != StatusPublished {
			return Result{}, ErrForbidden
		}
		q.Statuses = []string{StatusPublished}
		q.Audiences = []string{AudienceEmployee}
		if p.View {
			q.Audiences = append(q.Audiences, AudienceInternal)
		}
	}
	return s.store.List(ctx, q)
}
