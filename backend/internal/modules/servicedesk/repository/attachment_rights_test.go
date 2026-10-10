package repository_test

import (
	"context"
	"errors"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
)

// Attachment rights follow the ticket abilities (ADR-0037): readers read, commenters attach while the ticket is
// open, only people who work the Queue see internal attachments and remove any file, strangers see nothing.
func TestAttachmentRightsFollowTicketAccess(t *testing.T) {
	e := newQEnv(t)
	ctx := context.Background()
	q := e.queue(application.QueuePublic)
	e.grant(q, user(e.u1, "work"), user(e.u2, "view"))
	tk := e.raise(e.employee(e.alice), q)

	cases := []struct {
		name string
		p    application.Principal
		want application.AttachmentRights
	}{
		{"reporter", e.employee(e.alice), application.AttachmentRights{Read: true, Attach: true}},
		{"work grant", e.employee(e.u1), application.AttachmentRights{Read: true, Attach: true, Internal: true, Moderate: true}},
		{"view grant", e.employee(e.u2), application.AttachmentRights{Read: true, Internal: true}},
	}
	for _, c := range cases {
		got, err := e.svc.AttachmentRights(ctx, c.p, tk.ID)
		if err != nil || got != c.want {
			t.Errorf("%s: rights %+v err %v, want %+v", c.name, got, err, c.want)
		}
	}
	if _, err := e.svc.AttachmentRights(ctx, e.employee(e.bob), tk.ID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("a stranger must not learn the ticket exists: %v", err)
	}

	if _, err := e.svc.Transition(ctx, e.c(e.u1), e.employee(e.u1), tk.ID, nil, application.OpResolve, application.Params{Reason: "fixed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Transition(ctx, e.c(e.alice), e.employee(e.alice), tk.ID, nil, application.OpClose, application.Params{}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []application.Principal{e.employee(e.alice), e.employee(e.u1)} {
		got, err := e.svc.AttachmentRights(ctx, p, tk.ID)
		if err != nil || !got.Read || got.Attach {
			t.Errorf("a closed ticket is readable but takes no new files: %+v %v", got, err)
		}
	}
}
