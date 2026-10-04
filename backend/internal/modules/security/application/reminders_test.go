package application

import (
	"context"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/jobs"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/notifications"
	"github.com/jackc/pgx/v5"
	"testing"
	"time"
)

const reminderFindingID = "00000000-0000-7000-8000-0000000000a1"
const reminderAdvisoryID = "00000000-0000-7000-8000-0000000000a2"
const reminderUserID = "00000000-0000-7000-8000-0000000000a3"

type reminderStore struct {
	Store
	review time.Time
}

func (s reminderStore) DueRiskReviews(_ context.Context, today time.Time, after string, _ int) ([]string, error) {
	if after != "" {
		return nil, nil
	}
	days := int(s.review.Sub(today.Truncate(24*time.Hour)).Hours() / 24)
	if days >= 0 && days <= 14 {
		return []string{reminderFindingID}, nil
	}
	return nil, nil
}
func (s reminderStore) GetFinding(context.Context, string) (Finding, error) {
	return Finding{ID: reminderFindingID, AdvisoryID: reminderAdvisoryID, Reference: "VUL-TEST", Status: FindingRiskAccepted, RiskReviewBy: &s.review}, nil
}
func (s reminderStore) LockFindingTx(ctx context.Context, _ pgx.Tx, id string) (Finding, error) {
	return s.GetFinding(ctx, id)
}
func (s reminderStore) GetAdvisory(context.Context, string) (Advisory, error) {
	return Advisory{ID: reminderAdvisoryID, Reference: "ADV-TEST", Status: AdvisoryApplicable}, nil
}
func (s reminderStore) InTx(ctx context.Context, fn func(pgx.Tx) error) error { return fn(nil) }

type reminderDirectory struct{}

func (reminderDirectory) ActiveUsers(context.Context, []string) (map[string]bool, error) {
	return map[string]bool{reminderUserID: true}, nil
}

type reminderHolders struct{}

func (reminderHolders) ActiveUsers(_ context.Context, after string, _ int) ([]string, error) {
	if after != "" {
		return nil, nil
	}
	return []string{reminderUserID}, nil
}

type reminderPermissions struct{}

func (reminderPermissions) Permissions(context.Context, string) (map[string]struct{}, error) {
	return map[string]struct{}{PermManage: {}}, nil
}

type reminderNotifier struct {
	seen map[string]notifications.Intent
}

func (n *reminderNotifier) Create(_ context.Context, _ pgx.Tx, in notifications.Intent) (bool, error) {
	if _, ok := n.seen[in.DedupeKey]; ok {
		return false, nil
	}
	n.seen[in.DedupeKey] = in
	return true, nil
}
func TestRiskReviewReminderIdempotency(t *testing.T) {
	review := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	today := review.AddDate(0, 0, -14)
	note := &reminderNotifier{seen: map[string]notifications.Intent{}}
	n := NewNotifications(reminderStore{review: review}, reminderDirectory{}, reminderHolders{}, reminderPermissions{}, note).WithClock(func() time.Time { return today })
	for i := 0; i < 2; i++ {
		if err := n.HandleRiskReminders(context.Background(), jobs.Job{}); err != nil {
			t.Fatal(err)
		}
	}
	if len(note.seen) != 1 {
		t.Fatalf("14-day duplicate: %d", len(note.seen))
	}
	today = review.AddDate(0, 0, -1)
	if err := n.HandleRiskReminders(context.Background(), jobs.Job{}); err != nil {
		t.Fatal(err)
	}
	if len(note.seen) != 2 {
		t.Fatalf("1-day reminder: %d", len(note.seen))
	}
	today = review.AddDate(0, 0, -7)
	late := &reminderNotifier{seen: map[string]notifications.Intent{}}
	nLate := NewNotifications(reminderStore{review: review}, reminderDirectory{}, reminderHolders{}, reminderPermissions{}, late).WithClock(func() time.Time { return today })
	if err := nLate.HandleRiskReminders(context.Background(), jobs.Job{}); err != nil || len(late.seen) != 1 {
		t.Fatalf("skipped 14-day run: %d %v", len(late.seen), err)
	}
	for _, intent := range note.seen {
		if intent.Category != RiskReviewCategory || intent.LinkID != reminderFindingID || intent.Params["title"] != "VUL-TEST · ADV-TEST" {
			t.Fatalf("reference-only intent: %+v", intent)
		}
	}
}
