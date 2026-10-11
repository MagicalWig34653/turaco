package repository_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/repository"
)

// externals is the Organization side of participants: which ids are active external accounts.
type externals map[string]bool

func (e externals) ActiveExternals(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = e[id]
	}
	return out, nil
}

func (e externals) SearchExternals(_ context.Context, text string, _ int) ([]application.ExternalAccount, error) {
	if strings.ToLower(text) != "vera" {
		return nil, nil
	}
	return []application.ExternalAccount{{UserID: "0198a7c0-1111-7222-8333-444455556666", DisplayName: "Vera Vendor", PartyName: "Acme"}}, nil
}

type participantEnv struct {
	*env
	svc    *application.Service
	vendor string
	ext    externals
}

func newParticipantEnv(t *testing.T) *participantEnv {
	e := newEnv(t)
	var vendor string
	if err := e.pool.QueryRow(context.Background(), `SELECT uuidv7()::text`).Scan(&vendor); err != nil {
		t.Fatal(err)
	}
	ext := externals{vendor: true}
	svc := e.svc.WithExternalAccounts(repository.New(e.pool), ext)
	return &participantEnv{env: e, svc: svc, vendor: vendor, ext: ext}
}

func (p *participantEnv) externalPrincipal() application.Principal {
	return application.Principal{UserID: p.vendor, External: true}
}

func TestExternalAccountSeesOnlyTicketsSharedWithIt(t *testing.T) {
	p := newParticipantEnv(t)
	ctx := context.Background()
	shared, other := p.raise(), p.raise()
	staff := p.staff()
	if _, err := p.svc.AddComment(ctx, p.c(p.agent), staff, shared.ID, "public answer", false); err != nil {
		t.Fatal(err)
	}
	if _, err := p.svc.AddComment(ctx, p.c(p.agent), staff, shared.ID, "internal note about the vendor contract", true); err != nil {
		t.Fatal(err)
	}
	// Nothing is shared yet: the Ticket does not exist for the external account.
	if _, err := p.svc.Get(ctx, p.externalPrincipal(), shared.ID); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("before sharing: %v", err)
	}
	if _, err := p.svc.AddParticipant(ctx, p.c(p.agent), staff, shared.ID, p.vendor, application.LevelComment, nil); err != nil {
		t.Fatalf("share: %v", err)
	}
	d, err := p.svc.Get(ctx, p.externalPrincipal(), shared.ID)
	if err != nil {
		t.Fatalf("participant get: %v", err)
	}
	if d.Ticket.Title != shared.Title || len(d.Comments) != 1 || d.Comments[0].Internal || d.Comments[0].Body != "public answer" {
		t.Errorf("a participant reads the Ticket and its public comments only: %+v", d)
	}
	tk := d.Ticket
	if tk.Queue != nil || tk.QueueID != "" || tk.QueueTeamID != nil || tk.AssigneeID != nil || tk.ReporterID != "" || tk.AffectedUserID != "" ||
		tk.AssetID != nil || tk.MajorIncidentID != nil || tk.DuplicateOfID != nil || tk.DeviceSnapshot != nil || tk.AffectedLocationID != nil || len(tk.Aliases) != 0 {
		t.Errorf("routing, people and linked records stay hidden: %+v", tk)
	}
	if len(d.Allowed) != 0 || !d.Abilities.Comment || d.Abilities.InternalComment || d.Abilities.Assign || d.Abilities.Transition || d.Abilities.MoveQueue {
		t.Errorf("a participant has no staff abilities: allowed=%v abilities=%+v", d.Allowed, d.Abilities)
	}
	// Another Ticket stays invisible, even for an external principal that carries the view flags by mistake.
	for _, pr := range []application.Principal{p.externalPrincipal(), {UserID: p.vendor, External: true, View: true, Manage: true}} {
		if _, err := p.svc.Get(ctx, pr, other.ID); !errors.Is(err, application.ErrNotFound) {
			t.Errorf("unshared Ticket for %+v: %v", pr, err)
		}
	}
	greedy := application.Principal{UserID: p.vendor, External: true, View: true, Manage: true}
	// The list of all Tickets is closed to an external account (it has no Queue access whatever the flags say).
	if res, err := p.svc.List(ctx, greedy, true, application.Filter{Page: application.Page{Limit: 50}}); !errors.Is(err, application.ErrForbidden) || len(res.Items) != 0 {
		t.Errorf("list of all Tickets for an external account = %d items, %v", len(res.Items), err)
	}
	if res, err := p.svc.List(ctx, p.externalPrincipal(), false, application.Filter{Page: application.Page{Limit: 50}}); err != nil || len(res.Items) != 0 {
		t.Errorf("own list for an external account = %d items, %v", len(res.Items), err)
	}
	if _, err := p.svc.Create(ctx, p.c(p.vendor), greedy, application.CreateInput{Title: "Raised by a vendor"}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("external accounts raise no Tickets: %v", err)
	}
	if _, err := p.svc.Participants(ctx, greedy, shared.ID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("an external account does not list participants: %v", err)
	}
}

func TestParticipantCommentsArePublicAndNeedTheCommentLevel(t *testing.T) {
	p := newParticipantEnv(t)
	ctx := context.Background()
	tk := p.raise()
	staff := p.staff()
	if _, err := p.svc.AddParticipant(ctx, p.c(p.agent), staff, tk.ID, p.vendor, application.LevelRead, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := p.svc.AddComment(ctx, p.c(p.vendor), p.externalPrincipal(), tk.ID, "may I?", false); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("read level comments: %v", err)
	}
	if d, _ := p.svc.Get(ctx, p.externalPrincipal(), tk.ID); d.Abilities.Comment {
		t.Error("the read level offers no comment ability")
	}
	if _, err := p.svc.AddParticipant(ctx, p.c(p.agent), staff, tk.ID, p.vendor, application.LevelComment, nil); err != nil {
		t.Fatal(err)
	}
	c, err := p.svc.AddComment(ctx, p.c(p.vendor), p.externalPrincipal(), tk.ID, "fixed on our side", false)
	if err != nil || c.Internal || c.AuthorID != p.vendor {
		t.Fatalf("participant comment = %+v %v", c, err)
	}
	if _, err := p.svc.AddComment(ctx, p.c(p.vendor), p.externalPrincipal(), tk.ID, "secret", true); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("internal note by a participant: %v", err)
	}
	if _, err := p.svc.AddCommentWithMentions(ctx, p.c(p.vendor), p.externalPrincipal(), tk.ID, "hi", false, []string{p.alice}); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("mentions by a participant: %v", err)
	}
	if p.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'servicedesk.ticket.comment_added' AND metadata->>'participant' = 'true'`, p.corr) != 1 {
		t.Error("the participant comment is audited as such")
	}
	// A Ticket that is not shared answers not found, whatever the body.
	other := p.raise()
	if _, err := p.svc.AddComment(ctx, p.c(p.vendor), p.externalPrincipal(), other.ID, "hello", false); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("comment on an unshared Ticket: %v", err)
	}
	// Customers see the vendor's public comment as any other public comment.
	d, _ := p.svc.Get(ctx, p.user(), tk.ID)
	found := false
	for _, cm := range d.Comments {
		found = found || cm.Body == "fixed on our side"
	}
	if !found {
		t.Error("the requester sees the participant's public comment")
	}
}

func TestSharingNeedsWorkAccessAndAnActiveExternalAccount(t *testing.T) {
	p := newParticipantEnv(t)
	ctx := context.Background()
	tk := p.raise()
	// The requester owns the Ticket but does not work its Queue.
	if _, err := p.svc.AddParticipant(ctx, p.c(p.alice), p.user(), tk.ID, p.vendor, application.LevelRead, nil); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("requester shares: %v", err)
	}
	// Somebody with view access only does not share either.
	if _, err := p.svc.AddParticipant(ctx, p.c(p.viewer), p.view(), tk.ID, p.vendor, application.LevelRead, nil); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("viewer shares: %v", err)
	}
	staff := p.staff()
	for name, uid := range map[string]string{"employee": p.alice, "unknown": "0198a7c0-9999-7222-8333-444455556666"} {
		if _, err := p.svc.AddParticipant(ctx, p.c(p.agent), staff, tk.ID, uid, application.LevelRead, nil); !errors.Is(err, application.ErrParticipantInvalid) {
			t.Errorf("%s as participant: %v", name, err)
		}
	}
	var inv *application.InvalidInputError
	if _, err := p.svc.AddParticipant(ctx, p.c(p.agent), staff, tk.ID, p.vendor, "admin", nil); !errors.As(err, &inv) {
		t.Errorf("unknown level: %v", err)
	}
	past := time.Now().Add(-time.Hour)
	far := time.Now().AddDate(0, 0, application.MaxParticipantDays+1)
	for name, at := range map[string]time.Time{"past": past, "too far": far} {
		if _, err := p.svc.AddParticipant(ctx, p.c(p.agent), staff, tk.ID, p.vendor, application.LevelRead, &at); !errors.As(err, &inv) {
			t.Errorf("expiry %s: %v", name, err)
		}
	}
	// Sharing is idempotent per account: a second call changes the level, it does not add a row.
	if _, err := p.svc.AddParticipant(ctx, p.c(p.agent), staff, tk.ID, p.vendor, application.LevelRead, nil); err != nil {
		t.Fatal(err)
	}
	soon := time.Now().Add(time.Hour)
	if _, err := p.svc.AddParticipant(ctx, p.c(p.agent), staff, tk.ID, p.vendor, application.LevelComment, &soon); err != nil {
		t.Fatal(err)
	}
	list, err := p.svc.Participants(ctx, staff, tk.ID)
	if err != nil || len(list) != 1 || list[0].Level != application.LevelComment || list[0].ExpiresAt == nil {
		t.Fatalf("participants = %+v %v", list, err)
	}
	if _, err := p.svc.Participants(ctx, p.user(), tk.ID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("a requester does not list participants: %v", err)
	}
	if n := p.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'servicedesk.ticket.participant_added'`, p.corr); n != 2 {
		t.Errorf("participant_added audit events = %d, want 2", n)
	}
	// Candidates: staff with work access search; nobody else.
	found, err := p.svc.ParticipantCandidates(ctx, staff, tk.ID, "Vera")
	if err != nil || len(found) != 1 || found[0].PartyName != "Acme" {
		t.Errorf("candidates = %+v %v", found, err)
	}
	if _, err := p.svc.ParticipantCandidates(ctx, p.user(), tk.ID, "Vera"); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("requester searches candidates: %v", err)
	}
	// A closed Ticket is not shared any further.
	closed := p.raise()
	if _, err := p.svc.Transition(ctx, p.c(p.agent), staff, closed.ID, nil, application.OpCancel, application.Params{Reason: "duplicate"}); err != nil {
		t.Fatal(err)
	}
	var tr *application.InvalidTransitionError
	if _, err := p.svc.AddParticipant(ctx, p.c(p.agent), staff, closed.ID, p.vendor, application.LevelRead, nil); !errors.As(err, &tr) {
		t.Errorf("share a cancelled Ticket: %v", err)
	}
}

func TestRemovingAndExpiringAParticipation(t *testing.T) {
	p := newParticipantEnv(t)
	ctx := context.Background()
	tk := p.raise()
	staff := p.staff()
	soon := time.Now().Add(time.Hour)
	if _, err := p.svc.AddParticipant(ctx, p.c(p.agent), staff, tk.ID, p.vendor, application.LevelComment, &soon); err != nil {
		t.Fatal(err)
	}
	if _, err := p.svc.Get(ctx, p.externalPrincipal(), tk.ID); err != nil {
		t.Fatal(err)
	}
	// The share lapses without any job: the read checks the expiry.
	if _, err := p.pool.Exec(ctx, `UPDATE servicedesk.ticket_participants SET expires_at = now() - interval '1 second' WHERE ticket_id = $1::uuid`, tk.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.svc.Get(ctx, p.externalPrincipal(), tk.ID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("expired share: %v", err)
	}
	if _, err := p.svc.AddComment(ctx, p.c(p.vendor), p.externalPrincipal(), tk.ID, "late", false); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("comment after the share lapsed: %v", err)
	}
	// Sharing again renews it; removing ends it for good.
	if _, err := p.svc.AddParticipant(ctx, p.c(p.agent), staff, tk.ID, p.vendor, application.LevelRead, nil); err != nil {
		t.Fatal(err)
	}
	if err := p.svc.RemoveParticipant(ctx, p.c(p.alice), p.user(), tk.ID, p.vendor); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("requester removes: %v", err)
	}
	if err := p.svc.RemoveParticipant(ctx, p.c(p.agent), staff, tk.ID, p.vendor); err != nil {
		t.Fatal(err)
	}
	if _, err := p.svc.Get(ctx, p.externalPrincipal(), tk.ID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("after removal: %v", err)
	}
	if err := p.svc.RemoveParticipant(ctx, p.c(p.agent), staff, tk.ID, p.vendor); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("removing twice: %v", err)
	}
	if n := p.count(`SELECT count(*) FROM platform.audit_events WHERE correlation_id = $1 AND action = 'servicedesk.ticket.participant_removed'`, p.corr); n != 1 {
		t.Errorf("participant_removed audit events = %d, want 1", n)
	}
}

func TestParticipantLimit(t *testing.T) {
	p := newParticipantEnv(t)
	ctx := context.Background()
	tk := p.raise()
	staff := p.staff()
	for i := 0; i < application.MaxParticipants; i++ {
		var id string
		if err := p.pool.QueryRow(ctx, `SELECT uuidv7()::text`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		p.ext[id] = true
		if _, err := p.svc.AddParticipant(ctx, p.c(p.agent), staff, tk.ID, id, application.LevelRead, nil); err != nil {
			t.Fatalf("participant %d: %v", i, err)
		}
	}
	if _, err := p.svc.AddParticipant(ctx, p.c(p.agent), staff, tk.ID, p.vendor, application.LevelRead, nil); !errors.Is(err, application.ErrParticipantLimit) {
		t.Errorf("one too many: %v", err)
	}
}

func TestParticipantsNeedTheExternalDirectory(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tk := e.raise()
	// A service without WithExternalAccounts shares nothing and shows nothing to external accounts.
	if _, err := e.svc.AddParticipant(ctx, e.c(e.agent), e.staff(), tk.ID, e.alice, application.LevelRead, nil); !errors.Is(err, application.ErrForbidden) {
		t.Errorf("share without participants configured: %v", err)
	}
	ex := application.Principal{UserID: e.alice, External: true}
	if _, err := e.svc.Get(ctx, ex, tk.ID); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("external get without participants configured: %v", err)
	}
}
