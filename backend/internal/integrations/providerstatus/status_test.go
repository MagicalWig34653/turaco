package providerstatus

import (
	"testing"
	"time"
)

func TestTrackerIsUnverifiedUntilASuccessAndNeverKeepsFreeText(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	tr := NewTracker(func() time.Time { return now })
	if s := tr.Status(); s.State != Unverified || s.LastSuccessAt != nil {
		t.Fatalf("%+v", s)
	}
	tr.Failure("HTTP 403: tenant contoso says no!")
	if s := tr.Status(); s.State != Failing || s.LastErrorCode != SafeCode("HTTP 403: tenant contoso says no!") || len(s.LastErrorCode) > 40 {
		t.Fatalf("%+v", s)
	}
	tr.Success()
	if s := tr.Status(); s.State != Verified || s.LastSuccessAt == nil || !s.LastSuccessAt.Equal(now) || s.LastErrorCode != "" || s.LastFailureAt == nil {
		t.Fatalf("%+v", s)
	}
}

func TestSafeCode(t *testing.T) {
	for in, want := range map[string]string{"http_403": "http_403", "Invalid-Client!": "invalidclient", "": "error", "!!!": "error"} {
		if got := SafeCode(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
	if got := SafeCode("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); len(got) != 40 {
		t.Errorf("%d", len(got))
	}
}
