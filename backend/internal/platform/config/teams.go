package config

import (
	"fmt"
	"os"
	"strings"
)

// TeamsConfig is the configuration of the Microsoft Teams channel (ADR-0036, slice T-A).
type TeamsConfig struct {
	// DestinationsFile (TEAMS_CHANNEL_DESTINATIONS_FILE) is a deployment secret file: a JSON object of destination
	// key to Workflows webhook URL. Empty means Teams is not configured.
	DestinationsFile string
	// HTTPProxy and CAFile are the shared Microsoft client options (MICROSOFT_HTTP_PROXY, MICROSOFT_CA_FILE).
	HTTPProxy string
	CAFile    string
}

// Configured reports whether a destinations file is set.
func (c TeamsConfig) Configured() bool { return c.DestinationsFile != "" }

// LoadTeams reads the Teams settings. The file content is validated by the adapter at startup.
func LoadTeams() (TeamsConfig, error) {
	c := TeamsConfig{
		DestinationsFile: strings.TrimSpace(os.Getenv("TEAMS_CHANNEL_DESTINATIONS_FILE")),
		HTTPProxy:        strings.TrimSpace(os.Getenv("MICROSOFT_HTTP_PROXY")),
		CAFile:           strings.TrimSpace(os.Getenv("MICROSOFT_CA_FILE")),
	}
	if c.DestinationsFile != "" {
		if _, err := os.Stat(c.DestinationsFile); err != nil {
			return TeamsConfig{}, fmt.Errorf("TEAMS_CHANNEL_DESTINATIONS_FILE is not readable")
		}
	}
	return c, nil
}
