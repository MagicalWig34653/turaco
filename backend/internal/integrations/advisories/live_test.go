package advisories_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/advisories"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/advisories/cisakev"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/advisories/nvd"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/advisories/osv"
)

// The live smoke tests talk to the public NVD, OSV and CISA services. They are skipped unless
// TURACO_LIVE_FEED_TESTS=1 (see docs/integrations/advisory-feeds.md); NVD_API_KEY_FILE is optional.
func liveOnly(t *testing.T) {
	t.Helper()
	if os.Getenv("TURACO_LIVE_FEED_TESTS") != "1" {
		t.Skip("set TURACO_LIVE_FEED_TESTS=1 to run the live feed smoke tests")
	}
}

func TestLiveNVD(t *testing.T) {
	liveOnly(t)
	cfg := nvd.Config{MaxRecords: 2000, PageSize: 100, Version: "live-test"}
	if path := os.Getenv("NVD_API_KEY_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		cfg.APIKey = string(b)
	}
	c, err := nvd.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	res, err := c.Sync(ctx, time.Now().Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("nvd: %d records, complete=%v", len(res.Records), res.Complete)
	for _, r := range res.Records {
		if r.ExternalID == "" || r.Title == "" || r.SourceURL == "" {
			t.Fatalf("incomplete record %+v", r)
		}
	}
	if got, err := c.ByID(ctx, []string{"CVE-2021-44228"}); err != nil || len(got) != 1 || got[0].Severity != "critical" {
		t.Fatalf("by id: %v %v", got, err)
	}
}

func TestLiveCISAKEV(t *testing.T) {
	liveOnly(t)
	c, err := cisakev.New(cisakev.Config{Version: "live-test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	first, err := c.Catalog(ctx, "")
	if err != nil || len(first.Entries) < 1000 {
		t.Fatalf("catalog: %d entries, %v", len(first.Entries), err)
	}
	if first.ETag != "" {
		again, err := c.Catalog(ctx, first.ETag)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("conditional fetch: notModified=%v", again.NotModified)
	}
}

func TestLiveOSV(t *testing.T) {
	liveOnly(t)
	c, err := osv.New(osv.Config{MaxRecords: 50, Version: "live-test"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := c.Packages(ctx, []advisories.PackageQuery{{Ecosystem: "PyPI", Name: "jinja2"}})
	if err != nil || len(res.Records) == 0 {
		t.Fatalf("osv: %d records, %v", len(res.Records), err)
	}
	for _, r := range res.Records {
		if r.ExternalID == "" || r.Title == "" || r.SourceURL == "" {
			t.Fatalf("incomplete record %+v", r)
		}
	}
	if got, err := c.ByID(ctx, []string{res.Records[0].ExternalID}); err != nil || len(got) != 1 {
		t.Fatalf("by id: %v %v", got, err)
	}
}
