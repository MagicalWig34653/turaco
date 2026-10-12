package repository_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

type slaDir struct{}

func (slaDir) ActiveUsers(_ context.Context, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}
func (slaDir) ActiveTeams(context.Context, []string) (map[string]bool, error) { return nil, nil }
func (slaDir) UserNames(context.Context, []string) (map[string]string, error) { return nil, nil }
func (slaDir) TeamNames(context.Context, []string) (map[string]string, error) { return nil, nil }

type slaNotes struct{ intents []notifications.Intent }

func (n *slaNotes) Create(_ context.Context, _ pgx.Tx, in notifications.Intent) (bool, error) {
	n.intents = append(n.intents, in)
	return true, nil
}

type slaRecipientsFake struct {
	members []string
	blind   map[string]bool
}

func (r slaRecipientsFake) TeamMemberIDs(context.Context, string) ([]string, error) {
	return r.members, nil
}
func (r slaRecipientsFake) MaySeeQueue(_ context.Context, _ pgx.Tx, userID, _ string) (bool, error) {
	return !r.blind[userID], nil
}

func slaEvent(t *testing.T, ticketID string, occurred time.Time) events.OutboxEvent {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"ticketId": ticketID, "queueId": "q", "clockKind": "resolution", "dueAt": occurred, "assigneeId": nil})
	if err != nil {
		t.Fatal(err)
	}
	return events.OutboxEvent{ID: "00000000-0000-7000-8000-0000000000aa", EventType: "TicketSLABreached", OccurredAt: occurred, CorrelationID: "sweep-test", Payload: raw}
}

func TestSLANotificationsGoToAssigneeOrDefaultTeamAndDropStaleOrEndedClocks(t *testing.T) {
	ctx := context.Background()
	f := newSweepFixture(t, false, nil)
	var team string
	if err := f.pool.QueryRow(ctx, `INSERT INTO organization.teams(name) VALUES ('notify team '||right(uuidv7()::text,8)) RETURNING id::text`).Scan(&team); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE servicedesk.queues SET default_team_id=$2 WHERE id=$1`, f.queueID, team); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	id := f.ticket(now.Add(-3 * time.Hour))
	recipients := slaRecipientsFake{members: []string{"u-1", "u-2", "u-blind"}, blind: map[string]bool{"u-blind": true}}
	run := func(ev events.OutboxEvent, at time.Time) []notifications.Intent {
		t.Helper()
		notes := &slaNotes{}
		consumers := application.NewConsumers(f.repo, slaDir{}, notes).WithSLA(recipients, func() time.Time { return at })
		tx, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		if err := consumers.OnTicketSLABreached(ctx, tx, ev); err != nil {
			t.Fatal(err)
		}
		return notes.intents
	}

	// Unassigned: the default Team members who can view the Queue, not the ones who cannot.
	got := run(slaEvent(t, id, now), now)
	if len(got) != 2 || got[0].Category != "ticket.sla_breached" || got[0].LinkID != id {
		t.Fatalf("default team recipients: %+v", got)
	}
	// Assigned: only the assignee.
	if _, err := f.pool.Exec(ctx, `UPDATE servicedesk.tickets SET assignee_user_id=$2 WHERE id=$1`, id, sweepUser); err != nil {
		t.Fatal(err)
	}
	if got = run(slaEvent(t, id, now), now); len(got) != 1 || got[0].RecipientUserID != sweepUser {
		t.Fatalf("assignee recipients: %+v", got)
	}
	// An event older than one hour is dropped.
	if got = run(slaEvent(t, id, now.Add(-61*time.Minute)), now); len(got) != 0 {
		t.Fatalf("stale event notified: %+v", got)
	}
	if got = run(slaEvent(t, id, now.Add(-59*time.Minute)), now); len(got) != 1 {
		t.Fatalf("event within the hour must notify: %+v", got)
	}
	// A clock that no longer runs is dropped.
	f.apply(id, "resolve", "", now)
	if got = run(slaEvent(t, id, now), now); len(got) != 0 {
		t.Fatalf("completed clock notified: %+v", got)
	}
}
