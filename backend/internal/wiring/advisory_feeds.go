package wiring

import (
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/advisories"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/advisories/cisakev"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/advisories/msrc"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/advisories/nvd"
	"github.com/MagicalWig34653/turaco/backend/internal/integrations/advisories/osv"
	securityapp "github.com/MagicalWig34653/turaco/backend/internal/modules/security/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
)

// FeedMaxRecords bounds the NVD and OSV records of one sync run.
const FeedMaxRecords = 2000

// FeedUserAgentVersion is the version in the User-Agent "turaco/<version>" of the feed clients.
const FeedUserAgentVersion = "dev"

// SecurityWithFeeds builds the Security service with the configured advisory feed adapters (NVD, OSV, CISA KEV).
// The optional NVD API key is read from its file here and handed to the client only.
func SecurityWithFeeds(pool *pgxpool.Pool, cfg config.AdvisoryFeedConfig) (*securityapp.Service, error) {
	var feeds securityapp.FeedSources
	if slices.Contains(cfg.Sources, config.AdvisorySourceNVD) {
		ncfg := nvd.Config{Version: FeedUserAgentVersion, MaxRecords: FeedMaxRecords}
		if cfg.NVDAPIKeyFile != "" {
			key, err := config.ReadSecretFile(cfg.NVDAPIKeyFile)
			if err != nil {
				return nil, fmt.Errorf("NVD_API_KEY_FILE: %w", err)
			}
			ncfg.APIKey = key
		}
		client, err := nvd.New(ncfg)
		if err != nil {
			return nil, err
		}
		feeds.NVD = client
	}
	if slices.Contains(cfg.Sources, config.AdvisorySourceOSV) {
		client, err := osv.New(osv.Config{Version: FeedUserAgentVersion, MaxRecords: FeedMaxRecords})
		if err != nil {
			return nil, err
		}
		feeds.OSV = client
		for _, p := range cfg.OSVPackages {
			feeds.OSVPackages = append(feeds.OSVPackages, advisories.PackageQuery{Ecosystem: p.Ecosystem, Name: p.Name})
		}
	}
	if slices.Contains(cfg.Sources, config.AdvisorySourceMSRC) {
		client, err := msrc.New(msrc.Config{Version: FeedUserAgentVersion})
		if err != nil {
			return nil, err
		}
		feeds.MSRC = client
	}
	if slices.Contains(cfg.Sources, config.AdvisorySourceCISAKEV) {
		client, err := cisakev.New(cisakev.Config{Version: FeedUserAgentVersion})
		if err != nil {
			return nil, err
		}
		feeds.KEV = client
	}
	return Security(pool).WithFeeds(feeds), nil
}
