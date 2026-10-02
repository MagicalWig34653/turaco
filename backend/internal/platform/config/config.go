package config

import (
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Environment string
	HTTPAddr    string
	DatabaseURL string
	S3Endpoint  string
	S3Region    string
	S3Bucket    string
	S3AccessKey string
	S3SecretKey string
	S3PathStyle bool
	LogLevel    string

	SessionIdleTimeout     time.Duration
	SessionAbsoluteTimeout time.Duration
	SessionCookieSecure    bool

	// DirectoryProviderKey is LDAP_PROVIDER_KEY when LDAP_URL is set and empty
	// otherwise. turaco-api only needs this to accept manual sync requests;
	// the worker loads and validates the full LDAPConfig with LoadLDAP, so
	// bind credentials are only required where they are used.
	DirectoryProviderKey string

	// AuthEmergencyLoginEnabled exposes POST /auth/emergency-login.
	AuthEmergencyLoginEnabled bool
	// TrustedProxies are the networks whose X-Forwarded-For header is
	// trusted when determining the client address (login throttling, audit).
	TrustedProxies []netip.Prefix

	// Kerberos configures Kerberos/SPNEGO login; it is disabled unless
	// KERBEROS_KEYTAB_FILE is set and requires the directory (LDAP_URL).
	Kerberos KerberosConfig
}

// KerberosConfig configures validation of SPNEGO tickets by turaco-api. Only
// the path of the keytab (a deployment secret) is configured, never key
// material. It is enabled when KeytabFile is set.
type KerberosConfig struct {
	KeytabFile string
	// ServicePrincipal is the service's principal without realm, for example
	// HTTP/turaco.example.local.
	ServicePrincipal string
	// Realm is the only accepted Kerberos realm, upper case.
	Realm string
	// MaxClockSkew bounds the clock difference and the replay window.
	MaxClockSkew time.Duration
}

// Enabled reports whether Kerberos login is configured.
func (c KerberosConfig) Enabled() bool { return c.KeytabFile != "" }

// MaxKerberosClockSkew is the largest accepted KERBEROS_MAX_CLOCK_SKEW: the
// replay cache only remembers tickets for this long, so a larger value widens
// the replay window.
const MaxKerberosClockSkew = 15 * time.Minute

// LDAPConfig configures one directory provider. turaco-worker loads it fully
// with LoadLDAP for synchronization; turaco-api loads only the connection
// settings with LoadLDAPConnection for password login. It is enabled when URL
// is set. The bind password is never part of the
// configuration values: only the path of a file holding it (a deployment
// secret such as a Docker secret) is configured.
type LDAPConfig struct {
	ProviderKey       string
	URL               string
	StartTLS          bool
	AllowPlaintext    bool // plain ldap:// without StartTLS; development only
	CAFile            string
	BindDN            string
	BindPasswordFile  string
	DirectoryType     string // "active_directory" or "openldap"
	UserBaseDN        string
	UserFilter        string
	GroupBaseDN       string
	GroupFilter       string
	SyncInterval      time.Duration
	SyncTimeout       time.Duration
	MaxMissingPercent int
}

// Enabled reports whether directory synchronization is configured.
func (c LDAPConfig) Enabled() bool { return c.URL != "" }

const (
	DirectoryTypeActiveDirectory = "active_directory"
	DirectoryTypeOpenLDAP        = "openldap"
)

var defaultLDAPFilters = map[string][2]string{
	DirectoryTypeActiveDirectory: {"(&(objectCategory=person)(objectClass=user))", "(objectClass=group)"},
	DirectoryTypeOpenLDAP:        {"(objectClass=inetOrgPerson)", "(objectClass=groupOfNames)"},
}

var (
	// kerberosRealmPattern accepts upper-case realm names (DNS-style, letters,
	// digits, dots, hyphens, underscores).
	kerberosRealmPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9._-]{0,254}$`)
	// kerberosServicePattern accepts exactly service/host without a realm.
	kerberosServicePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)
)

var providerKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type Descriptor struct {
	Name        string
	Type        string
	Default     string
	Required    bool
	Secret      bool
	Description string
}

var Registry = []Descriptor{
	{Name: "APP_ENV", Type: "string", Default: "development", Description: "Runtime environment name."},
	{Name: "HTTP_ADDR", Type: "string", Default: ":8080", Description: "HTTP listen address for turaco-api."},
	{Name: "DATABASE_URL", Type: "string", Required: true, Secret: true, Description: "PostgreSQL connection URL."},
	{Name: "S3_ENDPOINT", Type: "string", Description: "S3-compatible endpoint; set for non-AWS/local providers."},
	{Name: "S3_REGION", Type: "string", Default: "us-east-1", Description: "S3 region."},
	{Name: "S3_BUCKET", Type: "string", Default: "turaco-dev", Description: "Object-storage bucket/namespace."},
	{Name: "S3_ACCESS_KEY_ID", Type: "string", Secret: true, Description: "S3 access key when required."},
	{Name: "S3_SECRET_ACCESS_KEY", Type: "string", Secret: true, Description: "S3 secret key when required."},
	{Name: "S3_PATH_STYLE", Type: "bool", Default: "true", Description: "Use path-style S3 addressing."},
	{Name: "LOG_LEVEL", Type: "string", Default: "info", Description: "Application log level."},
	{Name: "SESSION_IDLE_TIMEOUT", Type: "duration", Default: "8h", Description: "Session idle timeout; must be positive and not exceed SESSION_ABSOLUTE_TIMEOUT."},
	{Name: "SESSION_ABSOLUTE_TIMEOUT", Type: "duration", Default: "24h", Description: "Maximum session lifetime regardless of activity; must be positive."},
	{Name: "SESSION_COOKIE_SECURE", Type: "bool", Default: "true", Description: "Set the Secure attribute on the session cookie; disable only for local plain-HTTP development."},
	{Name: "AUTH_EMERGENCY_LOGIN_ENABLED", Type: "bool", Default: "false", Description: "Expose POST /api/v1/auth/emergency-login for the local break-glass account (created with turaco-admin). Every use is audited and logged at error level."},
	{Name: "HTTP_TRUSTED_PROXIES", Type: "string", Description: "Comma-separated CIDR prefixes of reverse proxies (for example the turaco-web container network) whose X-Forwarded-For header is trusted for the client address used by login throttling and audit. Empty trusts no proxy."},
	{Name: "KERBEROS_KEYTAB_FILE", Type: "string", Secret: true, Description: "Path to the keytab file (a deployment secret, mounted only into turaco-api) holding the key of KERBEROS_SERVICE_PRINCIPAL. Setting it enables Kerberos/SPNEGO login (GET /api/v1/auth/kerberos) and requires LDAP_URL: tickets are mapped to synced directory accounts. Empty disables Kerberos."},
	{Name: "KERBEROS_SERVICE_PRINCIPAL", Type: "string", Description: "Service principal of turaco-api without realm, for example `HTTP/turaco.example.local`; its key must be in the keytab. Required when KERBEROS_KEYTAB_FILE is set."},
	{Name: "KERBEROS_REALM", Type: "string", Description: "The only accepted Kerberos realm, upper case (for example `EXAMPLE.LOCAL`); tickets of other realms are refused. Required when KERBEROS_KEYTAB_FILE is set."},
	{Name: "KERBEROS_MAX_CLOCK_SKEW", Type: "duration", Default: "5m", Description: "Maximum clock difference between client and server accepted for Kerberos tickets, which also sets the replay-cache window; at most 15m."},
	{Name: "SMTP_HOST", Type: "string", Description: "Mail relay host name. Setting it enables the email channel of notifications in turaco-worker; empty disables email (in-app notifications are unaffected)."},
	{Name: "SMTP_PORT", Type: "int", Description: "Mail relay port. Default 587 for starttls, 465 for tls and 25 for none."},
	{Name: "SMTP_SECURITY", Type: "string", Default: "starttls", Description: "Connection protection: `starttls` (required upgrade before credentials or mail are sent), `tls` (implicit TLS) or `none` (clear text; accepted only with APP_ENV=development). Certificate verification is never disabled."},
	{Name: "SMTP_USERNAME", Type: "string", Description: "Relay account. Requires SMTP_PASSWORD_FILE."},
	{Name: "SMTP_PASSWORD_FILE", Type: "string", Secret: true, Description: "Path to a file containing the relay password (for example a Docker secret). Requires SMTP_USERNAME. The password is never stored in the database or logged."},
	{Name: "SMTP_CA_FILE", Type: "string", Description: "PEM file with CA certificates trusted for the relay, in addition to the system pool."},
	{Name: "SMTP_FROM", Type: "string", Description: "Sender address of notification emails, for example `Turaco <turaco@example.org>`. Required when SMTP_HOST is set."},
	{Name: "SMTP_TIMEOUT", Type: "duration", Default: "30s", Description: "Maximum duration of sending one email (connect, dialogue and transfer)."},
	{Name: "EMAIL_BASE_URL", Type: "string", Description: "Externally reachable address of the web application without a path, for example `https://turaco.example.org`; emails link to it. Required when SMTP_HOST is set; must be https outside development."},
	{Name: "EMAIL_DEFAULT_LOCALE", Type: "string", Default: "en", Description: "Language of notification emails: `en` or `de` (recipients have no language setting yet)."},
	{Name: "LDAP_URL", Type: "string", Description: "Directory server URL (`ldaps://host:636`, or `ldap://` with LDAP_START_TLS). Empty disables directory synchronization and password login."},
	{Name: "LDAP_PROVIDER_KEY", Type: "string", Default: "ad", Description: "Stable key identifying this directory in external identities and directory groups; lowercase letters, digits and hyphens. Changing it makes all existing observations stale."},
	{Name: "LDAP_START_TLS", Type: "bool", Default: "false", Description: "Upgrade an `ldap://` connection with StartTLS. Invalid boolean values are rejected."},
	{Name: "LDAP_ALLOW_PLAINTEXT", Type: "bool", Default: "false", Description: "Allow `ldap://` without StartTLS (credentials in clear text). Accepted only together with APP_ENV=development, for local test directories."},
	{Name: "LDAP_CA_FILE", Type: "string", Description: "PEM file with CA certificates trusted for the directory server, in addition to the system pool. Certificate verification is never disabled."},
	{Name: "LDAP_BIND_DN", Type: "string", Description: "DN of the read-only service account used for synchronization. Required when LDAP_URL is set."},
	{Name: "LDAP_BIND_PASSWORD_FILE", Type: "string", Secret: true, Description: "Path to a file containing the bind password (for example a Docker secret). Required when LDAP_URL is set. The password is never stored in the database or logged."},
	{Name: "LDAP_DIRECTORY_TYPE", Type: "string", Default: "active_directory", Description: "Directory schema: `active_directory` (objectGUID, sAMAccountName, userAccountControl) or `openldap` (entryUUID, uid)."},
	{Name: "LDAP_USER_BASE_DN", Type: "string", Description: "Search base for user accounts. Required when LDAP_URL is set."},
	{Name: "LDAP_USER_FILTER", Type: "string", Description: "User search filter. Default depends on LDAP_DIRECTORY_TYPE."},
	{Name: "LDAP_GROUP_BASE_DN", Type: "string", Description: "Search base for groups. Required when LDAP_URL is set."},
	{Name: "LDAP_GROUP_FILTER", Type: "string", Description: "Group search filter. Default depends on LDAP_DIRECTORY_TYPE."},
	{Name: "LDAP_SYNC_INTERVAL", Type: "duration", Default: "1h", Description: "Interval between scheduled directory synchronization runs; at least 5m."},
	{Name: "LDAP_SYNC_TIMEOUT", Type: "duration", Default: "15m", Description: "Maximum duration of one directory synchronization run; a run still marked running after this is treated as abandoned."},
	{Name: "LDAP_SYNC_MAX_MISSING_PERCENT", Type: "int", Default: "10", Description: "Safeguard: when more than this percentage (and more than 5) of the provider's active directory users, or of its observed groups, are missing from a run, that not-observed sweep is withheld (outcome sweep_withheld); everything else, including explicit disables, is applied. 0-100."},
}

func Load() (Config, error) {
	cfg := Config{
		Environment: getenv("APP_ENV", "development"),
		HTTPAddr:    getenv("HTTP_ADDR", ":8080"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		S3Endpoint:  os.Getenv("S3_ENDPOINT"),
		S3Region:    getenv("S3_REGION", "us-east-1"),
		S3Bucket:    getenv("S3_BUCKET", "turaco-dev"),
		S3AccessKey: os.Getenv("S3_ACCESS_KEY_ID"),
		S3SecretKey: os.Getenv("S3_SECRET_ACCESS_KEY"),
		S3PathStyle: getenv("S3_PATH_STYLE", "true") != "false",
		LogLevel:    getenv("LOG_LEVEL", "info"),

		SessionCookieSecure: getenv("SESSION_COOKIE_SECURE", "true") != "false",
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	var err error
	if cfg.SessionIdleTimeout, err = getDuration("SESSION_IDLE_TIMEOUT", 8*time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.SessionAbsoluteTimeout, err = getDuration("SESSION_ABSOLUTE_TIMEOUT", 24*time.Hour); err != nil {
		return Config{}, err
	}
	if cfg.SessionIdleTimeout < 2*time.Minute {
		return Config{}, fmt.Errorf("SESSION_IDLE_TIMEOUT must be at least 2m so sessions can slide")
	}
	if cfg.SessionIdleTimeout > cfg.SessionAbsoluteTimeout {
		return Config{}, fmt.Errorf("SESSION_IDLE_TIMEOUT must not exceed SESSION_ABSOLUTE_TIMEOUT")
	}
	if os.Getenv("LDAP_URL") != "" {
		cfg.DirectoryProviderKey = getenv("LDAP_PROVIDER_KEY", "ad")
		if !providerKeyPattern.MatchString(cfg.DirectoryProviderKey) {
			return Config{}, fmt.Errorf("LDAP_PROVIDER_KEY must match %s", providerKeyPattern)
		}
	}
	if cfg.AuthEmergencyLoginEnabled, err = getBool("AUTH_EMERGENCY_LOGIN_ENABLED", false); err != nil {
		return Config{}, err
	}
	if cfg.Kerberos, err = loadKerberos(cfg.DirectoryProviderKey != ""); err != nil {
		return Config{}, err
	}
	if cfg.TrustedProxies, err = getPrefixes("HTTP_TRUSTED_PROXIES"); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// loadKerberos reads and validates the Kerberos settings. The other settings
// are ignored while KERBEROS_KEYTAB_FILE is empty. It does not touch the
// file system: the keytab is checked when the validator is built.
func loadKerberos(directoryConfigured bool) (KerberosConfig, error) {
	c := KerberosConfig{KeytabFile: os.Getenv("KERBEROS_KEYTAB_FILE")}
	if c.KeytabFile == "" {
		return KerberosConfig{}, nil
	}
	if !directoryConfigured {
		return KerberosConfig{}, fmt.Errorf("KERBEROS_KEYTAB_FILE requires LDAP_URL: Kerberos principals are mapped to synced directory accounts")
	}
	c.ServicePrincipal = os.Getenv("KERBEROS_SERVICE_PRINCIPAL")
	c.Realm = os.Getenv("KERBEROS_REALM")
	if c.ServicePrincipal == "" {
		return KerberosConfig{}, fmt.Errorf("KERBEROS_SERVICE_PRINCIPAL is required when KERBEROS_KEYTAB_FILE is set")
	}
	if c.Realm == "" {
		return KerberosConfig{}, fmt.Errorf("KERBEROS_REALM is required when KERBEROS_KEYTAB_FILE is set")
	}
	if !kerberosRealmPattern.MatchString(c.Realm) {
		return KerberosConfig{}, fmt.Errorf("KERBEROS_REALM must be an upper-case realm name such as EXAMPLE.LOCAL")
	}
	if !kerberosServicePattern.MatchString(c.ServicePrincipal) {
		return KerberosConfig{}, fmt.Errorf("KERBEROS_SERVICE_PRINCIPAL must be service/host without a realm, for example HTTP/turaco.example.local")
	}
	var err error
	if c.MaxClockSkew, err = getDuration("KERBEROS_MAX_CLOCK_SKEW", 5*time.Minute); err != nil {
		return KerberosConfig{}, err
	}
	if c.MaxClockSkew > MaxKerberosClockSkew {
		return KerberosConfig{}, fmt.Errorf("KERBEROS_MAX_CLOCK_SKEW must not exceed %s", MaxKerberosClockSkew)
	}
	return c, nil
}

// getPrefixes parses a comma-separated list of CIDR prefixes or addresses.
func getPrefixes(name string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(getenv(name, ""), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if p, err := netip.ParsePrefix(part); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(part)
		if err != nil {
			return nil, fmt.Errorf("%s: invalid prefix or address %q", name, part)
		}
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// LoadLDAPConnection loads and validates only the settings needed to talk to
// the directory (provider key, URL, TLS, directory type). turaco-api uses it
// for password login, which binds as the user and therefore needs no bind
// credentials. environment is APP_ENV, which decides whether plain LDAP is
// allowed.
func LoadLDAPConnection(environment string) (LDAPConfig, error) {
	c := LDAPConfig{
		ProviderKey:   getenv("LDAP_PROVIDER_KEY", "ad"),
		URL:           os.Getenv("LDAP_URL"),
		CAFile:        os.Getenv("LDAP_CA_FILE"),
		DirectoryType: getenv("LDAP_DIRECTORY_TYPE", DirectoryTypeActiveDirectory),
	}
	if !c.Enabled() {
		return c, nil
	}
	var err error
	if c.StartTLS, err = getBool("LDAP_START_TLS", false); err != nil {
		return LDAPConfig{}, err
	}
	if c.AllowPlaintext, err = getBool("LDAP_ALLOW_PLAINTEXT", false); err != nil {
		return LDAPConfig{}, err
	}
	if _, ok := defaultLDAPFilters[c.DirectoryType]; !ok {
		return LDAPConfig{}, fmt.Errorf("LDAP_DIRECTORY_TYPE must be %q or %q", DirectoryTypeActiveDirectory, DirectoryTypeOpenLDAP)
	}
	if !providerKeyPattern.MatchString(c.ProviderKey) {
		return LDAPConfig{}, fmt.Errorf("LDAP_PROVIDER_KEY must match %s", providerKeyPattern)
	}
	u, perr := url.Parse(c.URL)
	if perr != nil || u.Host == "" {
		return LDAPConfig{}, fmt.Errorf("LDAP_URL must be an ldaps:// or ldap:// URL with a host")
	}
	switch u.Scheme {
	case "ldaps":
		if c.StartTLS {
			return LDAPConfig{}, fmt.Errorf("LDAP_START_TLS must not be combined with ldaps://")
		}
	case "ldap":
		// Fail closed: plain LDAP needs both an explicit opt-in and an explicit
		// development environment (APP_ENV defaults to development).
		if !c.StartTLS && !(c.AllowPlaintext && environment == "development") {
			return LDAPConfig{}, fmt.Errorf("LDAP_URL with ldap:// requires LDAP_START_TLS=true (plain LDAP needs LDAP_ALLOW_PLAINTEXT=true and APP_ENV=development)")
		}
	default:
		return LDAPConfig{}, fmt.Errorf("LDAP_URL must use ldaps:// or ldap://")
	}
	return c, nil
}

// LoadLDAP loads and validates the complete directory configuration for the
// process that synchronizes the directory (turaco-worker): the connection
// settings plus bind credentials, search bases/filters and sync policy.
func LoadLDAP(environment string) (LDAPConfig, error) {
	c, err := LoadLDAPConnection(environment)
	if err != nil || !c.Enabled() {
		return c, err
	}
	c.BindDN = os.Getenv("LDAP_BIND_DN")
	c.BindPasswordFile = os.Getenv("LDAP_BIND_PASSWORD_FILE")
	c.UserBaseDN = os.Getenv("LDAP_USER_BASE_DN")
	c.GroupBaseDN = os.Getenv("LDAP_GROUP_BASE_DN")
	filters := defaultLDAPFilters[c.DirectoryType]
	c.UserFilter = getenv("LDAP_USER_FILTER", filters[0])
	c.GroupFilter = getenv("LDAP_GROUP_FILTER", filters[1])
	// Only emptiness flows into the error, never a configured value.
	for _, required := range []struct {
		name    string
		missing bool
	}{
		{"LDAP_BIND_DN", c.BindDN == ""}, {"LDAP_BIND_PASSWORD_FILE", c.BindPasswordFile == ""},
		{"LDAP_USER_BASE_DN", c.UserBaseDN == ""}, {"LDAP_GROUP_BASE_DN", c.GroupBaseDN == ""},
	} {
		if required.missing {
			return LDAPConfig{}, fmt.Errorf("%s is required when LDAP_URL is set", required.name)
		}
	}
	if c.SyncInterval, err = getDuration("LDAP_SYNC_INTERVAL", time.Hour); err != nil {
		return LDAPConfig{}, err
	}
	if c.SyncInterval < 5*time.Minute {
		return LDAPConfig{}, fmt.Errorf("LDAP_SYNC_INTERVAL must be at least 5m")
	}
	if c.SyncTimeout, err = getDuration("LDAP_SYNC_TIMEOUT", 15*time.Minute); err != nil {
		return LDAPConfig{}, err
	}
	pct := getenv("LDAP_SYNC_MAX_MISSING_PERCENT", "10")
	if c.MaxMissingPercent, err = strconv.Atoi(pct); err != nil || c.MaxMissingPercent < 0 || c.MaxMissingPercent > 100 {
		return LDAPConfig{}, fmt.Errorf("LDAP_SYNC_MAX_MISSING_PERCENT must be an integer between 0 and 100")
	}
	return c, nil
}

func getDuration(name string, fallback time.Duration) (time.Duration, error) {
	raw := getenv(name, "")
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration %q: %w", name, raw, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be greater than zero", name)
	}
	return d, nil
}

func getBool(name string, fallback bool) (bool, error) {
	raw := getenv(name, "")
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s: invalid boolean %q", name, raw)
	}
	return v, nil
}

func getenv(name, fallback string) string {
	if value, ok := os.LookupEnv(name); ok && value != "" {
		return value
	}
	return fallback
}
