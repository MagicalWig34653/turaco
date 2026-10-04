package application

import (
	"context"
	"errors"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"testing"
	"time"
)

func TestResidualRisk(t *testing.T) {
	cases := []struct {
		severity            string
		probable, potential int
		want                string
	}{
		{"critical", 0, 0, "none"}, {"low", 0, 1, "low"}, {"high", 0, 1, "medium"}, {"medium", 1, 0, "medium"}, {"high", 1, 0, "high"}, {"critical", 1, 2, "high"},
	}
	for _, c := range cases {
		if got := ResidualRisk(c.severity, c.probable, c.potential); got != c.want {
			t.Errorf("%s %d/%d = %s, want %s", c.severity, c.probable, c.potential, got, c.want)
		}
	}
}
func TestRemediationAuthorizationAndVersion(t *testing.T) {
	s := NewService(nil, nil)
	c := Caller{Actor: audit.UserActor("00000000-0000-7000-8000-000000000001"), CorrelationID: "test"}
	p := Principal{UserID: c.Actor.UserID, Manage: true}
	due := time.Now().Add(24 * time.Hour)
	if _, err := s.CreateRemediationTask(context.Background(), c, Principal{}, "advisory", "", nil, nil, nil, &due); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := s.CreateRemediationTask(context.Background(), c, p, "advisory", "", nil, nil, nil, &due); err == nil {
		t.Fatal("expectedVersion required")
	}
	version := 1
	user := c.Actor.UserID
	validID := "00000000-0000-7000-8000-000000000002"
	tooLate := time.Now().AddDate(1, 0, 1)
	if _, err := s.CreateRemediationTask(context.Background(), c, p, "advisory", validID, &version, &user, nil, &tooLate); err == nil {
		t.Fatal("task due beyond one year allowed")
	}
	if _, err := s.CreateRemediationTask(context.Background(), c, p, "advisory", validID, &version, nil, nil, &due); err == nil {
		t.Fatal("task without assignee allowed")
	}
	if _, err := s.LinkChange(context.Background(), c, Principal{}, "", "", nil); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := s.LinkChange(context.Background(), c, p, "", "", nil); err == nil {
		t.Fatal("expectedVersion required for link")
	}
	if err := s.UnlinkChange(context.Background(), c, p, "", "", nil); err == nil {
		t.Fatal("expectedVersion required")
	}
	if _, err := s.RemediationTasks(context.Background(), Principal{}, "advisory", ""); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := s.LinkedChanges(context.Background(), Principal{}, ""); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
}
