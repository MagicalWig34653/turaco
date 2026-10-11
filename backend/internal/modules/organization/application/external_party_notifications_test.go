package application

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/events"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
)

type notificationPartyStore struct{ context PartyNotificationContext }

func (s *notificationPartyStore) AccountContextTx(context.Context, pgx.Tx, string) (PartyNotificationContext, bool, error) {
	return PartyNotificationContext{}, false, nil
}
func (s *notificationPartyStore) PartyContextTx(context.Context, pgx.Tx, string) (PartyNotificationContext, bool, error) {
	return s.context, true, nil
}
func (s *notificationPartyStore) IsActiveEmployeeTx(context.Context, pgx.Tx, string) (bool, error) {
	return true, nil
}

type notificationRecorder struct{ intents []notifications.Intent }

func (n *notificationRecorder) Create(_ context.Context, _ pgx.Tx, in notifications.Intent) (bool, error) {
	n.intents = append(n.intents, in)
	return true, nil
}

func TestStalePartyEventsDoNotContradictCurrentState(t *testing.T) {
	store := &notificationPartyStore{context: PartyNotificationContext{PartyName: "Vendor", SponsorID: "A", PartyActive: true}}
	recorder := &notificationRecorder{}
	consumer := NewPartyConsumers(store, recorder)
	changed, _ := json.Marshal(map[string]string{"partyId": "party", "sponsorUserId": "B", "previousSponsorUserId": "A"})
	if err := consumer.OnSponsorChanged(context.Background(), nil, events.OutboxEvent{ID: "change", Payload: changed}); err != nil {
		t.Fatal(err)
	}
	if len(recorder.intents) != 0 {
		t.Errorf("delayed A->B after B->A: %+v", recorder.intents)
	}
	deactivated, _ := json.Marshal(map[string]string{"partyId": "party"})
	if err := consumer.OnPartyDeactivated(context.Background(), nil, events.OutboxEvent{ID: "deactivated", Payload: deactivated}); err != nil {
		t.Fatal(err)
	}
	if len(recorder.intents) != 0 {
		t.Errorf("delayed deactivation after reactivation: %+v", recorder.intents)
	}
}
