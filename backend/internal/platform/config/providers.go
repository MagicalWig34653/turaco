package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// GraphRegistration is one Microsoft Graph app registration used with the client credentials flow. Turaco has a
// read registration (MICROSOFT_GRAPH_*, Intune read synchronization) and a separate write registration
// (MICROSOFT_GRAPH_WRITE_*, deployment assignments); ADR-0027 and the F15 design keep them apart.
type GraphRegistration struct {
	// Configured is false when none of the keys is set; the provider then stays the "not configured" placeholder.
	Configured bool
	TenantID   string
	ClientID   string
	// Exactly one credential kind: SecretFile, or CertificateFile with PrivateKeyFile.
	SecretFile      string
	SecretExpiresAt *time.Time
	CertificateFile string
	PrivateKeyFile  string
	// HTTPProxy and CAFile are the shared Microsoft client options.
	HTTPProxy string
	CAFile    string
}

// LoadGraphRead reads the Graph read registration (MICROSOFT_GRAPH_*).
func LoadGraphRead() (GraphRegistration, error) { return loadGraph("MICROSOFT_GRAPH_") }

// LoadGraphWrite reads the Graph write registration (MICROSOFT_GRAPH_WRITE_*).
func LoadGraphWrite() (GraphRegistration, error) { return loadGraph("MICROSOFT_GRAPH_WRITE_") }

// IntuneGraphBeta reports whether the Intune read client may call Graph beta endpoints (INTUNE_GRAPH_BETA).
func IntuneGraphBeta() (bool, error) { return getBool("INTUNE_GRAPH_BETA", false) }

func loadGraph(prefix string) (GraphRegistration, error) {
	env := func(name string) string { return strings.TrimSpace(os.Getenv(prefix + name)) }
	g := GraphRegistration{
		TenantID: strings.ToLower(env("TENANT_ID")), ClientID: strings.ToLower(env("CLIENT_ID")),
		SecretFile: env("CLIENT_SECRET_FILE"), CertificateFile: env("CLIENT_CERTIFICATE_FILE"), PrivateKeyFile: env("CLIENT_PRIVATE_KEY_FILE"),
		HTTPProxy: strings.TrimSpace(os.Getenv("MICROSOFT_HTTP_PROXY")), CAFile: strings.TrimSpace(os.Getenv("MICROSOFT_CA_FILE")),
	}
	g.Configured = g.TenantID != "" || g.ClientID != "" || g.SecretFile != "" || g.CertificateFile != "" || g.PrivateKeyFile != ""
	if !g.Configured {
		return g, nil
	}
	if !entraGUID.MatchString(g.TenantID) {
		return GraphRegistration{}, fmt.Errorf("%sTENANT_ID must be the tenant GUID", prefix)
	}
	if !entraGUID.MatchString(g.ClientID) {
		return GraphRegistration{}, fmt.Errorf("%sCLIENT_ID must be the application (client) GUID", prefix)
	}
	hasCert := g.CertificateFile != "" || g.PrivateKeyFile != ""
	switch {
	case g.SecretFile != "" && hasCert:
		return GraphRegistration{}, fmt.Errorf("configure either %sCLIENT_SECRET_FILE or the certificate files, not both", prefix)
	case g.SecretFile == "" && !hasCert:
		return GraphRegistration{}, fmt.Errorf("%sCLIENT_SECRET_FILE or %sCLIENT_CERTIFICATE_FILE with %sCLIENT_PRIVATE_KEY_FILE is required", prefix, prefix, prefix)
	case hasCert && (g.CertificateFile == "" || g.PrivateKeyFile == ""):
		return GraphRegistration{}, fmt.Errorf("%sCLIENT_CERTIFICATE_FILE and %sCLIENT_PRIVATE_KEY_FILE must be set together", prefix, prefix)
	}
	if raw := env("CLIENT_SECRET_EXPIRES_AT"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return GraphRegistration{}, fmt.Errorf("%sCLIENT_SECRET_EXPIRES_AT must be an RFC 3339 timestamp", prefix)
		}
		g.SecretExpiresAt = &t
	}
	return g, nil
}

// AutotaskCredentials is the configuration of the Autotask PSA REST client (AUTOTASK_*). Picklist values are
// tenant specific (Admin > Features & Settings), so Turaco's statuses and priorities are mapped explicitly.
type AutotaskCredentials struct {
	// Configured is false when none of the keys is set; the gateway then stays the "not configured" placeholder.
	Configured bool
	Username   string
	// SecretFile holds the API user's secret (deployment secret file, ADR-0014).
	SecretFile      string
	IntegrationCode string
	// CompanyID is the Autotask company (customer) that receives the tickets; QueueID is optional.
	CompanyID int64
	QueueID   int64
	// StatusMap and PriorityMap map Turaco values to Autotask picklist values.
	StatusMap   map[string]int
	PriorityMap map[string]int
	// RateLimitPerHour is Turaco's own ceiling of requests per rolling hour, below Autotask's limit of 10,000 per
	// database that all integrations share.
	RateLimitPerHour int
}

var (
	autotaskStatuses   = []string{"new", "open", "in_progress", "waiting", "resolved", "closed", "cancelled"}
	autotaskPriorities = []string{"low", "normal", "high", "urgent"}
)

// LoadAutotask reads and validates the Autotask REST client settings.
func LoadAutotask() (AutotaskCredentials, error) {
	env := func(name string) string { return strings.TrimSpace(os.Getenv(name)) }
	a := AutotaskCredentials{Username: env("AUTOTASK_API_USERNAME"), SecretFile: env("AUTOTASK_API_SECRET_FILE"), IntegrationCode: env("AUTOTASK_INTEGRATION_CODE")}
	keys := []string{"AUTOTASK_COMPANY_ID", "AUTOTASK_QUEUE_ID", "AUTOTASK_STATUS_MAP", "AUTOTASK_PRIORITY_MAP"}
	a.Configured = a.Username != "" || a.SecretFile != "" || a.IntegrationCode != ""
	for _, k := range keys {
		a.Configured = a.Configured || env(k) != ""
	}
	if !a.Configured {
		return a, nil
	}
	for name, v := range map[string]string{"AUTOTASK_API_USERNAME": a.Username, "AUTOTASK_API_SECRET_FILE": a.SecretFile, "AUTOTASK_INTEGRATION_CODE": a.IntegrationCode} {
		if v == "" {
			return AutotaskCredentials{}, fmt.Errorf("%s is required when the Autotask client is configured", name)
		}
	}
	if !strings.Contains(a.Username, "@") || len(a.Username) > 200 || strings.ContainsAny(a.Username, " \r\n\t/?#&") {
		return AutotaskCredentials{}, fmt.Errorf("AUTOTASK_API_USERNAME must be the API user's login name (an email address)")
	}
	if len(a.IntegrationCode) > 100 || strings.ContainsAny(a.IntegrationCode, " \r\n\t") {
		return AutotaskCredentials{}, fmt.Errorf("AUTOTASK_INTEGRATION_CODE is not a valid integration code")
	}
	var err error
	if a.CompanyID, err = positiveInt64("AUTOTASK_COMPANY_ID", env("AUTOTASK_COMPANY_ID"), true); err != nil {
		return AutotaskCredentials{}, err
	}
	if a.QueueID, err = positiveInt64("AUTOTASK_QUEUE_ID", env("AUTOTASK_QUEUE_ID"), false); err != nil {
		return AutotaskCredentials{}, err
	}
	if a.StatusMap, err = pickMap("AUTOTASK_STATUS_MAP", env("AUTOTASK_STATUS_MAP"), autotaskStatuses); err != nil {
		return AutotaskCredentials{}, err
	}
	if a.PriorityMap, err = pickMap("AUTOTASK_PRIORITY_MAP", env("AUTOTASK_PRIORITY_MAP"), autotaskPriorities); err != nil {
		return AutotaskCredentials{}, err
	}
	a.RateLimitPerHour = 3000
	if raw := env("AUTOTASK_RATE_LIMIT_PER_HOUR"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 10 || n > 9000 {
			return AutotaskCredentials{}, fmt.Errorf("AUTOTASK_RATE_LIMIT_PER_HOUR must be between 10 and 9000")
		}
		a.RateLimitPerHour = n
	}
	return a, nil
}

func positiveInt64(name, raw string, required bool) (int64, error) {
	if raw == "" {
		if required {
			return 0, fmt.Errorf("%s is required when the Autotask client is configured", name)
		}
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return n, nil
}

// pickMap parses "key=id,key=id" and requires every key of want exactly once.
func pickMap(name, raw string, want []string) (map[string]int, error) {
	out := map[string]int{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		k = strings.TrimSpace(k)
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if !ok || err != nil || n <= 0 {
			return nil, fmt.Errorf("%s: %q is not key=id with a positive integer id", name, part)
		}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("%s: %q is listed twice", name, k)
		}
		out[k] = n
	}
	for _, k := range want {
		if _, ok := out[k]; !ok {
			return nil, fmt.Errorf("%s must map %s", name, strings.Join(want, ", "))
		}
	}
	if len(out) != len(want) {
		return nil, fmt.Errorf("%s contains an unknown key (known: %s)", name, strings.Join(want, ", "))
	}
	return out, nil
}
