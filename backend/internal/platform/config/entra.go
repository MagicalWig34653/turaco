package config

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Entra tenant modes of ENTRA_TENANT_MODE.
const (
	EntraTenantSingle          = "single"
	EntraTenantMultiRestricted = "multi_restricted"
)

var entraGUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// EntraConfig is the configuration of the Microsoft Entra ID sign-in (ADR-0035, slice E-A).
type EntraConfig struct {
	// Enabled mirrors AUTH_ENTRA_LOGIN_ENABLED.
	Enabled bool
	// TenantMode is EntraTenantSingle or EntraTenantMultiRestricted.
	TenantMode string
	// TenantID is the home tenant (single mode) and the tenant bound to the directory in hybrid setups.
	TenantID string
	// AllowedTenantIDs lists the accepted tenants of the multi_restricted mode (always contains TenantID in single mode).
	AllowedTenantIDs []string
	ClientID         string
	// ClientSecretFile, ClientCertificateFile and ClientPrivateKeyFile are deployment secret files; exactly one credential kind.
	ClientSecretFile      string
	ClientSecretExpiresAt *time.Time
	ClientCertificateFile string
	ClientPrivateKeyFile  string
	// RedirectURL is the callback exactly as registered in Entra; it is never derived from request headers.
	RedirectURL string
	// MaxClockSkew is the tolerance for exp, nbf and iat.
	MaxClockSkew time.Duration
	// SessionMaxAge caps the absolute lifetime of an Entra session (the administration setting auth.session_absolute_timeout may lower it).
	SessionMaxAge time.Duration
	// HTTPProxy and CAFile are the shared Microsoft client options (MICROSOFT_HTTP_PROXY, MICROSOFT_CA_FILE).
	HTTPProxy string
	CAFile    string
	// LinkDirectoryProviderKey (ENTRA_LINK_DIRECTORY_PROVIDER_KEY) binds ENTRA_TENANT_ID to the synchronized on-prem
	// directory provider: at sign-in the Entra source anchor (onPremisesImmutableId) is matched against that
	// provider's external subjects. Empty disables the hybrid match and the Graph /me call (and the User.Read scope).
	LinkDirectoryProviderKey string
	// Provisioning is not here: auth.entra_provisioning is a runtime setting.
}

// EntraEnabled reports whether the sign-in is switched on.
func (c EntraConfig) EntraEnabled() bool { return c.Enabled }

// LoadEntra reads and validates the Entra sign-in settings. With AUTH_ENTRA_LOGIN_ENABLED unset or false nothing else is validated.
func LoadEntra() (EntraConfig, error) {
	var c EntraConfig
	var err error
	if c.Enabled, err = getBool("AUTH_ENTRA_LOGIN_ENABLED", false); err != nil {
		return EntraConfig{}, err
	}
	c.HTTPProxy = strings.TrimSpace(os.Getenv("MICROSOFT_HTTP_PROXY"))
	c.CAFile = strings.TrimSpace(os.Getenv("MICROSOFT_CA_FILE"))
	if !c.Enabled {
		return c, nil
	}
	if cloud := getenv("ENTRA_CLOUD", "global"); cloud != "global" {
		return EntraConfig{}, fmt.Errorf("ENTRA_CLOUD %q is not supported: only the global cloud is built", cloud)
	}
	c.TenantMode = getenv("ENTRA_TENANT_MODE", EntraTenantSingle)
	if c.TenantMode != EntraTenantSingle && c.TenantMode != EntraTenantMultiRestricted {
		return EntraConfig{}, fmt.Errorf("ENTRA_TENANT_MODE must be %q or %q", EntraTenantSingle, EntraTenantMultiRestricted)
	}
	c.TenantID = strings.ToLower(strings.TrimSpace(os.Getenv("ENTRA_TENANT_ID")))
	for _, part := range strings.Split(os.Getenv("ENTRA_ALLOWED_TENANT_IDS"), ",") {
		if part = strings.ToLower(strings.TrimSpace(part)); part != "" && !slices.Contains(c.AllowedTenantIDs, part) {
			c.AllowedTenantIDs = append(c.AllowedTenantIDs, part)
		}
	}
	switch c.TenantMode {
	case EntraTenantSingle:
		if !entraGUID.MatchString(c.TenantID) {
			return EntraConfig{}, fmt.Errorf("ENTRA_TENANT_ID must be the tenant GUID when ENTRA_TENANT_MODE=single")
		}
		c.AllowedTenantIDs = []string{c.TenantID}
	case EntraTenantMultiRestricted:
		if len(c.AllowedTenantIDs) == 0 {
			return EntraConfig{}, fmt.Errorf("ENTRA_ALLOWED_TENANT_IDS must list at least one tenant GUID when ENTRA_TENANT_MODE=multi_restricted")
		}
		if c.TenantID != "" && !entraGUID.MatchString(c.TenantID) {
			return EntraConfig{}, fmt.Errorf("ENTRA_TENANT_ID must be a GUID")
		}
	}
	for _, t := range c.AllowedTenantIDs {
		if !entraGUID.MatchString(t) {
			return EntraConfig{}, fmt.Errorf("ENTRA_ALLOWED_TENANT_IDS must contain tenant GUIDs only")
		}
		// The Microsoft consumer tenant is never accepted.
		if t == "9188040d-6c67-4c5b-b112-36a304b66dad" {
			return EntraConfig{}, fmt.Errorf("ENTRA_ALLOWED_TENANT_IDS must not contain the Microsoft consumer tenant")
		}
	}
	c.ClientID = strings.ToLower(strings.TrimSpace(os.Getenv("ENTRA_CLIENT_ID")))
	if !entraGUID.MatchString(c.ClientID) {
		return EntraConfig{}, fmt.Errorf("ENTRA_CLIENT_ID must be the application (client) GUID")
	}
	c.ClientSecretFile = strings.TrimSpace(os.Getenv("ENTRA_CLIENT_SECRET_FILE"))
	c.ClientCertificateFile = strings.TrimSpace(os.Getenv("ENTRA_CLIENT_CERTIFICATE_FILE"))
	c.ClientPrivateKeyFile = strings.TrimSpace(os.Getenv("ENTRA_CLIENT_PRIVATE_KEY_FILE"))
	hasCert := c.ClientCertificateFile != "" || c.ClientPrivateKeyFile != ""
	switch {
	case c.ClientSecretFile != "" && hasCert:
		return EntraConfig{}, fmt.Errorf("configure either ENTRA_CLIENT_SECRET_FILE or the certificate files, not both")
	case c.ClientSecretFile == "" && !hasCert:
		return EntraConfig{}, fmt.Errorf("ENTRA_CLIENT_SECRET_FILE or ENTRA_CLIENT_CERTIFICATE_FILE with ENTRA_CLIENT_PRIVATE_KEY_FILE is required")
	case hasCert && (c.ClientCertificateFile == "" || c.ClientPrivateKeyFile == ""):
		return EntraConfig{}, fmt.Errorf("ENTRA_CLIENT_CERTIFICATE_FILE and ENTRA_CLIENT_PRIVATE_KEY_FILE must be set together")
	}
	if raw := strings.TrimSpace(os.Getenv("ENTRA_CLIENT_SECRET_EXPIRES_AT")); raw != "" {
		t, perr := time.Parse(time.RFC3339, raw)
		if perr != nil {
			return EntraConfig{}, fmt.Errorf("ENTRA_CLIENT_SECRET_EXPIRES_AT must be an RFC 3339 timestamp")
		}
		c.ClientSecretExpiresAt = &t
	}
	c.LinkDirectoryProviderKey = strings.TrimSpace(os.Getenv("ENTRA_LINK_DIRECTORY_PROVIDER_KEY"))
	if c.LinkDirectoryProviderKey != "" {
		if !providerKeyPattern.MatchString(c.LinkDirectoryProviderKey) {
			return EntraConfig{}, fmt.Errorf("ENTRA_LINK_DIRECTORY_PROVIDER_KEY must be a provider key (lower-case letters, digits and hyphens)")
		}
		// The binding names exactly one tenant, which is always the home tenant (the match is never made for another tenant).
		if !entraGUID.MatchString(c.TenantID) {
			return EntraConfig{}, fmt.Errorf("ENTRA_TENANT_ID is required when ENTRA_LINK_DIRECTORY_PROVIDER_KEY is set")
		}
		if !slices.Contains(c.AllowedTenantIDs, c.TenantID) {
			return EntraConfig{}, fmt.Errorf("ENTRA_TENANT_ID must be listed in ENTRA_ALLOWED_TENANT_IDS when ENTRA_LINK_DIRECTORY_PROVIDER_KEY is set")
		}
	}
	c.RedirectURL = strings.TrimSpace(os.Getenv("ENTRA_REDIRECT_URL"))
	if err := validateEntraRedirect(c.RedirectURL, getenv("APP_ENV", "development")); err != nil {
		return EntraConfig{}, err
	}
	if c.MaxClockSkew, err = getDuration("ENTRA_MAX_CLOCK_SKEW", 2*time.Minute); err != nil {
		return EntraConfig{}, err
	}
	if c.MaxClockSkew <= 0 || c.MaxClockSkew > 5*time.Minute {
		return EntraConfig{}, fmt.Errorf("ENTRA_MAX_CLOCK_SKEW must be positive and at most 5m")
	}
	if c.SessionMaxAge, err = getDuration("ENTRA_SESSION_MAX_AGE", 8*time.Hour); err != nil {
		return EntraConfig{}, err
	}
	if c.SessionMaxAge < time.Hour || c.SessionMaxAge > 24*time.Hour {
		return EntraConfig{}, fmt.Errorf("ENTRA_SESSION_MAX_AGE must be between 1h and 24h")
	}
	if c.HTTPProxy != "" {
		u, perr := url.Parse(c.HTTPProxy)
		if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return EntraConfig{}, fmt.Errorf("MICROSOFT_HTTP_PROXY must be an http or https URL")
		}
	}
	return c, nil
}

// validateEntraRedirect requires the callback URL exactly as registered: https, no fragment, the fixed callback
// path; http only for localhost in development.
func validateEntraRedirect(raw, environment string) error {
	u, err := url.Parse(raw)
	if err != nil || raw == "" || u.Host == "" || u.Fragment != "" || u.RawQuery != "" || u.User != nil {
		return fmt.Errorf("ENTRA_REDIRECT_URL must be the absolute callback URL registered in Entra")
	}
	if u.Path != "/api/v1/auth/entra/callback" {
		return fmt.Errorf("ENTRA_REDIRECT_URL must end with /api/v1/auth/entra/callback")
	}
	switch u.Scheme {
	case "https":
	case "http":
		host := u.Hostname()
		if environment != "development" || (host != "localhost" && host != "127.0.0.1" && host != "::1") {
			return fmt.Errorf("ENTRA_REDIRECT_URL must use https (http is accepted only for localhost in development)")
		}
	default:
		return fmt.Errorf("ENTRA_REDIRECT_URL must use https")
	}
	return nil
}
