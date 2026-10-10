package teams

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
)

// DestinationKeyPattern is the shape of a destination key (also enforced by the database).
var DestinationKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// allowedWebhookSuffixes are the host suffixes a Workflows webhook URL may have (global cloud). They are stated as
// understood at design time and must be verified in a lab tenant (docs/integrations/teams.md).
var allowedWebhookSuffixes = []string{".logic.azure.com", ".api.powerplatform.com"}

const maxDestinationsFile = 64 << 10

// Destinations maps destination keys to Workflows webhook URLs. The URLs are secrets.
type Destinations map[string]string

// LoadDestinations reads TEAMS_CHANNEL_DESTINATIONS_FILE: a JSON object of destination key to webhook URL. Errors
// name the problem and the key, never a URL.
func LoadDestinations(path string) (Destinations, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read TEAMS_CHANNEL_DESTINATIONS_FILE: %w", errors.Unwrap(err))
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxDestinationsFile+1))
	if err != nil {
		return nil, fmt.Errorf("read TEAMS_CHANNEL_DESTINATIONS_FILE: %w", err)
	}
	if len(data) > maxDestinationsFile {
		return nil, errors.New("TEAMS_CHANNEL_DESTINATIONS_FILE is too large")
	}
	return ParseDestinations(data)
}

// ParseDestinations validates the file content.
func ParseDestinations(data []byte) (Destinations, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var raw map[string]string
	if err := dec.Decode(&raw); err != nil {
		return nil, errors.New("TEAMS_CHANNEL_DESTINATIONS_FILE must be a JSON object of destination key to webhook URL")
	}
	if dec.More() {
		return nil, errors.New("TEAMS_CHANNEL_DESTINATIONS_FILE has trailing content")
	}
	if len(raw) == 0 {
		return nil, errors.New("TEAMS_CHANNEL_DESTINATIONS_FILE holds no destination")
	}
	if len(raw) > 100 {
		return nil, errors.New("TEAMS_CHANNEL_DESTINATIONS_FILE holds too many destinations")
	}
	out := make(Destinations, len(raw))
	for key, rawURL := range raw {
		if !DestinationKeyPattern.MatchString(key) {
			return nil, fmt.Errorf("destination key %q must match %s", truncateKey(key), DestinationKeyPattern)
		}
		if err := validateWebhookURL(rawURL); err != nil {
			return nil, fmt.Errorf("destination %q: %w", key, err)
		}
		out[key] = rawURL
	}
	return out, nil
}

func truncateKey(k string) string {
	if len(k) > 40 {
		return k[:40]
	}
	return k
}

func validateWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("the webhook URL is not a valid URL")
	}
	if u.Scheme != "https" || u.User != nil || u.Fragment != "" {
		return errors.New("the webhook URL must be an https URL without credentials")
	}
	if p := u.Port(); p != "" && p != "443" {
		return errors.New("the webhook URL must use port 443")
	}
	host := strings.ToLower(u.Hostname())
	for _, suffix := range allowedWebhookSuffixes {
		if strings.HasSuffix(host, suffix) && len(host) > len(suffix) {
			return nil
		}
	}
	return errors.New("the webhook host is not a Power Automate Workflows host (*.logic.azure.com, *.api.powerplatform.com)")
}

// Keys returns the destination keys, sorted.
func (d Destinations) Keys() []string {
	out := make([]string, 0, len(d))
	for k := range d {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Hosts returns the distinct webhook host names, sorted; the HTTP client allows exactly these.
func (d Destinations) Hosts() []string {
	seen := map[string]bool{}
	var out []string
	for _, raw := range d {
		if u, err := url.Parse(raw); err == nil && !seen[strings.ToLower(u.Hostname())] {
			seen[strings.ToLower(u.Hostname())] = true
			out = append(out, strings.ToLower(u.Hostname()))
		}
	}
	sort.Strings(out)
	return out
}
