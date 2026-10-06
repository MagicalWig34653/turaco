package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/advisories"
)

func TestFromRecordAppendsBoundedReferences(t *testing.T) {
	in := FromRecord(advisories.AdvisoryRecord{Source: "nvd", ExternalID: "CVE-2026-1", Summary: "text", References: []string{"https://a.example/1", "https://a.example/2"}})
	if in.Summary != "text\n\nReferences:\nhttps://a.example/1\nhttps://a.example/2" {
		t.Fatalf("summary %q", in.Summary)
	}
	if got := FromRecord(advisories.AdvisoryRecord{References: []string{"https://a.example/1"}}).Summary; got != "References:\nhttps://a.example/1" {
		t.Fatalf("summary without text %q", got)
	}
	long := strings.Repeat("x", maxSummary-30)
	got := FromRecord(advisories.AdvisoryRecord{Summary: long, References: []string{"https://a.example/" + strings.Repeat("y", 100)}}).Summary
	if got != long {
		t.Fatal("references that do not fit the summary limit are dropped")
	}
	if a, err := cleanAdvisory(AdvisoryInput{Source: "nvd", ExternalID: "CVE-2026-1", Title: "t", Severity: "low", Summary: got}); err != nil || a.Summary == nil {
		t.Fatalf("%v", err)
	}
}

func TestFeedErrorCodesAreConstants(t *testing.T) {
	cases := map[error]string{
		advisories.ErrRateLimited:                   FeedErrRateLimited,
		advisories.ErrUnavailable:                   FeedErrUnavailable,
		advisories.ErrInvalidResponse:               FeedErrInvalid,
		context.DeadlineExceeded:                    FeedErrTimeout,
		errors.New("secret url https://x?apiKey=1"): FeedErrFailed,
	}
	for err, want := range cases {
		if got := feedErrorCode(err); got != want {
			t.Errorf("%v: %s, want %s", err, got, want)
		}
	}
}

func TestFeedSourceKeysOrder(t *testing.T) {
	f := FeedSources{}
	if len(f.FeedSourceKeys()) != 0 {
		t.Fatal("no sources configured")
	}
}
