package config

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
)

// Advisory feed source keys of ADVISORY_SOURCES.
const (
	AdvisorySourceNVD     = "nvd"
	AdvisorySourceOSV     = "osv"
	AdvisorySourceMSRC    = "msrc"
	AdvisorySourceCISAKEV = "cisa_kev"
)

// MaxOSVPackages bounds OSV_PACKAGES; every package costs at least one request per sync run.
const MaxOSVPackages = 200

// OSVPackage names one package of an OSV ecosystem (OSV_PACKAGES entry "ecosystem=name").
type OSVPackage struct {
	Ecosystem string
	Name      string
}

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
	// OSVPackages are the packages the osv source queries (OSV has no modified-since API); required when
	// the osv source is selected.
	OSVPackages []OSVPackage
	Interval    time.Duration
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
		case part != AdvisorySourceNVD && part != AdvisorySourceOSV && part != AdvisorySourceMSRC && part != AdvisorySourceCISAKEV:
			return AdvisoryFeedConfig{}, fmt.Errorf("ADVISORY_SOURCES: unknown source %q (use %s, %s, %s, %s)", part, AdvisorySourceNVD, AdvisorySourceOSV, AdvisorySourceMSRC, AdvisorySourceCISAKEV)
		case !slices.Contains(c.Sources, part):
			c.Sources = append(c.Sources, part)
		}
	}
	if len(c.Sources) == 0 {
		return AdvisoryFeedConfig{}, fmt.Errorf("ADVISORY_SOURCES must name at least one source")
	}
	// NVD is read before OSV and KEV is read last, so that fresh CVEs exist when the catalog enriches them.
	rank := map[string]int{AdvisorySourceNVD: 0, AdvisorySourceOSV: 1, AdvisorySourceMSRC: 2, AdvisorySourceCISAKEV: 3}
	slices.SortStableFunc(c.Sources, func(a, b string) int { return rank[a] - rank[b] })
	c.NVDAPIKeyFile = strings.TrimSpace(getenv("NVD_API_KEY_FILE", ""))
	if c.OSVPackages, err = parseOSVPackages(getenv("OSV_PACKAGES", "")); err != nil {
		return AdvisoryFeedConfig{}, err
	}
	if slices.Contains(c.Sources, AdvisorySourceOSV) && len(c.OSVPackages) == 0 {
		return AdvisoryFeedConfig{}, fmt.Errorf("OSV_PACKAGES must name at least one package when the %s source is selected", AdvisorySourceOSV)
	}
	if c.Interval, err = getDuration("ADVISORY_SYNC_INTERVAL", 6*time.Hour); err != nil {
		return AdvisoryFeedConfig{}, err
	}
	if c.Interval < MinAdvisorySyncInterval {
		return AdvisoryFeedConfig{}, fmt.Errorf("ADVISORY_SYNC_INTERVAL must be at least %s", MinAdvisorySyncInterval)
	}
	return c, nil
}

// parseOSVPackages reads the comma-separated "ecosystem=name" entries of OSV_PACKAGES (for example
// "npm=lodash,Maven=org.apache.logging.log4j:log4j-core,Debian:12=openssl"); the first "=" separates the
// ecosystem from the package name. Duplicates are dropped.
func parseOSVPackages(raw string) ([]OSVPackage, error) {
	var out []OSVPackage
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eco, name, ok := strings.Cut(part, "=")
		eco, name = strings.TrimSpace(eco), strings.TrimSpace(name)
		if !ok || eco == "" || name == "" || len(eco) > 60 || len(name) > 200 || strings.ContainsFunc(part, unicode.IsControl) {
			return nil, fmt.Errorf("OSV_PACKAGES: invalid entry %q (use ecosystem=name)", part)
		}
		p := OSVPackage{Ecosystem: eco, Name: name}
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	if len(out) > MaxOSVPackages {
		return nil, fmt.Errorf("OSV_PACKAGES: at most %d packages", MaxOSVPackages)
	}
	return out, nil
}
