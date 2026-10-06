package config

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Advisory feed source keys of ADVISORY_SOURCES.
const (
	AdvisorySourceNVD     = "nvd"
	AdvisorySourceCISAKEV = "cisa_kev"
)

// MinAdvisorySyncInterval is the shortest ADVISORY_SYNC_INTERVAL; the public feeds are rate limited and
// the data changes slowly.
const MinAdvisorySyncInterval = time.Hour

// AdvisoryFeedConfig is the configuration of the advisory feed synchronization (turaco-worker and
// turaco-admin security sync-feeds).
type AdvisoryFeedConfig struct {
	// Enabled schedules the sync job (ADVISORY_SYNC).
	Enabled bool
	// Sources lists the feeds to read, in run order, without duplicates.
	Sources []string
	// NVDAPIKeyFile is the optional file holding the NVD API key; the key itself is read by the caller.
	NVDAPIKeyFile string
	Interval      time.Duration
}

// LoadAdvisoryFeeds reads and validates the advisory feed settings.
func LoadAdvisoryFeeds() (AdvisoryFeedConfig, error) {
	var c AdvisoryFeedConfig
	var err error
	if c.Enabled, err = getBool("ADVISORY_SYNC", false); err != nil {
		return AdvisoryFeedConfig{}, err
	}
	for _, part := range strings.Split(getenv("ADVISORY_SOURCES", AdvisorySourceNVD+","+AdvisorySourceCISAKEV), ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		switch {
		case part == "":
			continue
		case part != AdvisorySourceNVD && part != AdvisorySourceCISAKEV:
			return AdvisoryFeedConfig{}, fmt.Errorf("ADVISORY_SOURCES: unknown source %q (use %s, %s)", part, AdvisorySourceNVD, AdvisorySourceCISAKEV)
		case !slices.Contains(c.Sources, part):
			c.Sources = append(c.Sources, part)
		}
	}
	if len(c.Sources) == 0 {
		return AdvisoryFeedConfig{}, fmt.Errorf("ADVISORY_SOURCES must name at least one source")
	}
	// NVD is read before KEV so that fresh CVEs exist when the catalog enriches them.
	slices.SortStableFunc(c.Sources, func(a, b string) int {
		switch {
		case a == b:
			return 0
		case a == AdvisorySourceNVD:
			return -1
		}
		return 1
	})
	c.NVDAPIKeyFile = strings.TrimSpace(getenv("NVD_API_KEY_FILE", ""))
	if c.Interval, err = getDuration("ADVISORY_SYNC_INTERVAL", 6*time.Hour); err != nil {
		return AdvisoryFeedConfig{}, err
	}
	if c.Interval < MinAdvisorySyncInterval {
		return AdvisoryFeedConfig{}, fmt.Errorf("ADVISORY_SYNC_INTERVAL must be at least %s", MinAdvisorySyncInterval)
	}
	return c, nil
}
