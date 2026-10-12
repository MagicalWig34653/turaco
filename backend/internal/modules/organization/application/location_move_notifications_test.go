package application

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

type fakeMoveStore struct {
	names  map[string]string
	active map[string]bool
}

func (f fakeMoveStore) LocationNameTx(_ context.Context, _ pgx.Tx, id string) (string, bool, error) {
	n, ok := f.names[id]
	return n, ok, nil
}

func (f fakeMoveStore) IsActiveEmployeeTx(_ context.Context, _ pgx.Tx, id string) (bool, error) {
	return f.active[id], nil
}

type fakeAdmins []string

func (f fakeAdmins) AdministratorUserIDs(context.Context, pgx.Tx) ([]string, error) { return f, nil }

type intents struct{ got []notifications.Intent }

func (n *intents) Create(_ context.Context, _ pgx.Tx, in notifications.Intent) (bool, error) {
	n.got = append(n.got, in)
	return true, nil
}

func TestScopedLocationMovedNotifiesOtherActiveAdministrators(t *testing.T) {
	store := fakeMoveStore{names: map[string]string{"loc": "Ward 3"}, active: map[string]bool{"actor": true, "other": true}}
	n := &intents{}
	c := NewLocationMoveConsumers(store, fakeAdmins{"actor", "other", "inactive"}, n)
	actor := "actor"
	ev := events.OutboxEvent{ID: "ev1", EventType: EventScopedLocationMoved, ActorID: &actor,
		Payload: []byte(`{"locationId":"loc","fromParentId":"a","toParentId":"b","scopedAssignmentsAffected":3}`)}
	if err := c.OnScopedLocationMoved(context.Background(), nil, ev); err != nil {
		t.Fatal(err)
	}
	if len(n.got) != 1 {
		t.Fatalf("intents = %+v", n.got)
	}
	in := n.got[0]
	if in.RecipientUserID != "other" || in.Category != NotificationScopedLocationMoved || in.LinkType != LinkLocationAdmin || in.LinkID != "loc" ||
		in.DedupeKey != "ev1:other" || in.Params["title"] != "Ward 3" || in.Params["count"] != 3 {
		t.Fatalf("intent = %+v", in)
	}
	// An unknown Location (deleted meanwhile) notifies nobody; a malformed payload is a permanent failure.
	n.got = nil
	if err := c.OnScopedLocationMoved(context.Background(), nil, events.OutboxEvent{ID: "ev2", Payload: []byte(`{"locationId":"gone"}`)}); err != nil || len(n.got) != 0 {
		t.Fatalf("unknown Location: %v %+v", err, n.got)
	}
	if err := c.OnScopedLocationMoved(context.Background(), nil, events.OutboxEvent{ID: "ev3", Payload: []byte(`[`)}); err == nil {
		t.Fatal("malformed payload accepted")
	}
}
