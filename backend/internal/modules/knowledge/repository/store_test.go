package repository_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/knowledge/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/knowledge/repository"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/database/dbtest"
)

const (
	editor   = "00000000-0000-7000-8000-0000000000e1"
	employee = "00000000-0000-7000-8000-0000000000e2"
	staff    = "00000000-0000-7000-8000-0000000000e3"
)

func newSvc(t *testing.T) (*application.Service, string, func(string) application.Caller) {
	t.Helper()
	pool := dbtest.Pool(t)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	tag := "kbtest" + hex.EncodeToString(b)
	corr := "knowledge-" + tag
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM platform.audit_events WHERE correlation_id = $1`, corr)
		_, _ = pool.Exec(ctx, `DELETE FROM platform.outbox_events WHERE correlation_id = $1`, corr)
		_, _ = pool.Exec(ctx, `DELETE FROM knowledge.articles WHERE title LIKE $1`, tag+"%")
	})
	return application.NewService(repository.New(pool)), tag, func(u string) application.Caller {
		return application.Caller{Actor: audit.UserActor(u), CorrelationID: corr}
	}
}

func TestArticleLifecycleVisibilityAndSearch(t *testing.T) {
	svc, tag, c := newSvc(t)
	ctx := context.Background()
	manage := application.Principal{UserID: editor, Manage: true}
	view := application.Principal{UserID: staff, View: true}
	user := application.Principal{UserID: employee}

	internal, err := svc.Create(ctx, c(editor), manage, application.Input{Title: tag + " Reset BitLocker recovery key", Summary: "Staff only", Body: "Escalate to the security team before revealing the key.", Audience: "internal"})
	if err != nil || internal.Status != "draft" || internal.Reference == "" {
		t.Fatalf("create = %+v %v", internal, err)
	}
	public, err := svc.Create(ctx, c(editor), manage, application.Input{Title: tag + " Connect to the guest wifi", Body: "Choose the guest network and accept the terms.", Audience: "employee"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, c(employee), user, application.Input{Title: "x", Body: "y"}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("create without knowledge.manage: %v", err)
	}
	var inv *application.InvalidInputError
	if _, err := svc.Create(ctx, c(editor), manage, application.Input{Title: tag, Body: ""}); !errors.As(err, &inv) {
		t.Errorf("empty body: %v", err)
	}
	if _, err := svc.Create(ctx, c(editor), manage, application.Input{Title: tag, Body: "b", Audience: "world"}); !errors.As(err, &inv) {
		t.Errorf("unknown audience: %v", err)
	}

	// Drafts are invisible to everyone but managers; unknown and hidden ids look the same.
	for _, p := range []application.Principal{user, view} {
		if _, err := svc.Get(ctx, p, public.ID); !errors.Is(err, application.ErrNotFound) {
			t.Errorf("draft read by %+v: %v", p, err)
		}
	}
	if _, err := svc.Publish(ctx, c(editor), manage, public.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Publish(ctx, c(editor), manage, internal.ID, nil); err != nil {
		t.Fatal(err)
	}
	var tr *application.InvalidTransitionError
	if _, err := svc.Publish(ctx, c(editor), manage, public.ID, nil); !errors.As(err, &tr) {
		t.Errorf("publishing twice: %v", err)
	}
	if _, err := svc.Get(ctx, user, public.ID); err != nil {
		t.Errorf("employee reads a published employee article: %v", err)
	}
	if _, err := svc.Get(ctx, user, internal.ID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("employee reads an internal article: %v", err)
	}
	if _, err := svc.Get(ctx, view, internal.ID); err != nil {
		t.Errorf("knowledge.view reads internal: %v", err)
	}

	// Search honours the same visibility and finds by any word of title, summary or body.
	hits := func(p application.Principal, q string) int {
		res, err := svc.List(ctx, p, q, "", application.Page{})
		if err != nil {
			t.Fatal(err)
		}
		return len(res.Items)
	}
	if hits(user, tag) != 1 || hits(view, tag) != 2 || hits(manage, tag) != 2 {
		t.Errorf("visible matches: employee %d, view %d, manage %d; want 1, 2, 2", hits(user, tag), hits(view, tag), hits(manage, tag))
	}
	if hits(user, "guest network") != 1 || hits(user, "bitlocker") != 0 || hits(view, "BitLocker") != 1 {
		t.Error("full-text search must respect case and visibility")
	}
	if _, err := svc.List(ctx, user, "", "draft", application.Page{}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("employee filters drafts: %v", err)
	}
	if hits(user, `") OR 1=1 --`) != 0 {
		t.Error("odd search text must not match everything")
	}

	// Editing and retiring.
	upd, err := svc.Update(ctx, c(editor), manage, public.ID, 2, application.Input{Title: tag + " Connect to guest wifi", Body: "Updated steps.", Audience: "employee"})
	if err != nil || upd.Version != 3 {
		t.Fatalf("update = %+v %v", upd, err)
	}
	if _, err := svc.Update(ctx, c(editor), manage, public.ID, 2, application.Input{Title: "x", Body: "y"}); !errors.Is(err, application.ErrVersionConflict) {
		t.Errorf("stale update: %v", err)
	}
	if _, err := svc.Retire(ctx, c(editor), manage, public.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx, user, public.ID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("retired article visible to employee: %v", err)
	}
	if _, err := svc.Update(ctx, c(editor), manage, public.ID, 4, application.Input{Title: "x", Body: "y"}); !errors.As(err, &tr) {
		t.Errorf("editing a retired article: %v", err)
	}
	if _, err := svc.Publish(ctx, c(editor), manage, public.ID, nil); err != nil {
		t.Errorf("republishing: %v", err)
	}
}
