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

var tenantIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

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

	// AutotaskSync turns on the outbound/inbound synchronization of tickets with Autotask.
	AutotaskSync bool
	// IntuneSync allows POST /api/v1/endpoint-sync to read devices from the Intune provider.
	IntuneSync bool
	// SoftwareProviderSync allows the Software Package synchronization (POST /api/v1/software/packages/sync and
	// the scheduled job) against the Software Management Provider.
	SoftwareProviderSync bool
	// SoftwareDeployWrite is the capability SOFTWARE_DEPLOY_WRITE: Deployments may write ring assignments to the
	// Management Provider (start, resume, promote) and the engine job runs.
	SoftwareDeployWrite bool

	// RemoteAccessProviders are the enabled Remote Access Provider keys (REMOTE_ACCESS_PROVIDERS); empty switches
	// the feature off. The composition roots build the connectors and refuse unknown keys.
	RemoteAccessProviders []string
	// RemoteAccessApprovalOwnership are the Device ownerships whose remote sessions need a second approver.
	RemoteAccessApprovalOwnership []string
	// PresenceEnabled is PRESENCE_ENABLED, the startup gate of Workforce Presence (ADR-0028); the runtime switch
	// and the recorded data protection dates are administrative settings.
	PresenceEnabled bool
	// PresenceRetentionDays is PRESENCE_RETENTION_DAYS (1 to 30), the upper bound of the runtime retention.
	PresenceRetentionDays int
	// AuditRetentionDays is AUDIT_RETENTION_DAYS: 0 keeps every audit event, otherwise events older than that many
	// days are purged by the worker (at least 365; the database function enforces the floor again).
	AuditRetentionDays int
	// PresenceSourceStaleAfter is PRESENCE_SOURCE_STALE_AFTER: older external signals count as unknown.
	PresenceSourceStaleAfter time.Duration

	// AIEnabled is AI_ENABLED, the startup gate of Turaco AI (ADR-0029, F12): without it only GET /ai/status and the admin configuration routes are
	// mounted and no provider is ever called. The runtime switch is the audited ai.settings.enabled.
	AIEnabled bool
	// AISecretDir is AI_SECRET_DIR: the directory of deployment secret files that AI Provider secret references name.
	AISecretDir string
	// TenantID is TENANT_ID, the fixed data plane id of this single-tenant installation (F12 A15).
	TenantID string

	// DirectoryProviderKey is LDAP_PROVIDER_KEY when LDAP_URL is set and empty
	// otherwise. turaco-api only needs this to accept manual sync requests;
	// the worker loads and validates the full LDAPConfig with LoadLDAP, so
	// bind credentials are only required where they are used.
	DirectoryProviderKey string

	// AuthEmergencyLoginEnabled exposes POST /auth/emergency-login.
	AuthEmergencyLoginEnabled bool
	// PeopleLookupEnabled is PEOPLE_LOOKUP_ENABLED: GET /api/v1/people/lookup for every signed-in employee (on-behalf pickers).
	PeopleLookupEnabled bool
	// AuthLocalLoginEnabled exposes POST /auth/local-login and the credential token redemption for local accounts.
	AuthLocalLoginEnabled bool
	// EmailBaseURL is EMAIL_BASE_URL (validated, no trailing slash); empty when unset. Invitation and reset links
	// are built only from it.
	EmailBaseURL string
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
	{Name: "PEOPLE_LOOKUP_ENABLED", Type: "bool", Default: "true", Description: "Enable GET /api/v1/people/lookup: every signed-in employee can find active internal colleagues by name (at least 3 characters, at most 10 results, only id, display name and department, rate limited, audited without the search text) to raise a support ticket or request for another person. Off answers 404; people with organization.view keep the directory routes."},
	{Name: "AUTH_LOCAL_LOGIN_ENABLED", Type: "bool", Default: "false", Description: "Enable local accounts (ADR-0034): People administration can invite Users who are not in the directory, and they sign in with POST /api/v1/auth/local-login after setting a password through a single-use link. Off disables local accounts completely (the endpoints answer 404 and existing local sessions are rejected). Needs EMAIL_BASE_URL for the links. Local accounts never hold high-risk permissions."},
	{Name: "AUTH_EMERGENCY_LOGIN_ENABLED", Type: "bool", Default: "false", Description: "Expose POST /api/v1/auth/emergency-login for the local break-glass account (created with turaco-admin). Every use is audited and logged at error level."},
	{Name: "HTTP_TRUSTED_PROXIES", Type: "string", Description: "Comma-separated CIDR prefixes of reverse proxies (for example the turaco-web container network) whose X-Forwarded-For header is trusted for the client address used by login throttling and audit. Empty trusts no proxy."},
	{Name: "KERBEROS_KEYTAB_FILE", Type: "string", Secret: true, Description: "Path to the keytab file (a deployment secret, mounted only into turaco-api) holding the key of KERBEROS_SERVICE_PRINCIPAL. Setting it enables Kerberos/SPNEGO login (GET /api/v1/auth/kerberos) and requires LDAP_URL: tickets are mapped to synced directory accounts. Empty disables Kerberos."},
	{Name: "KERBEROS_SERVICE_PRINCIPAL", Type: "string", Description: "Service principal of turaco-api without realm, for example `HTTP/turaco.example.local`; its key must be in the keytab. Required when KERBEROS_KEYTAB_FILE is set."},
	{Name: "KERBEROS_REALM", Type: "string", Description: "The only accepted Kerberos realm, upper case (for example `EXAMPLE.LOCAL`); tickets of other realms are refused. Required when KERBEROS_KEYTAB_FILE is set."},
	{Name: "KERBEROS_MAX_CLOCK_SKEW", Type: "duration", Default: "5m", Description: "Maximum clock difference between client and server accepted for Kerberos tickets, which also sets the replay-cache window; at most 15m."},
	{Name: "SMTP_HOST", Type: "string", Description: "Mail relay host name. Setting it enables the email channel of notifications in turaco-worker; empty disables email (in-app notifications are unaffected)."},
	{Name: "SMTP_PORT", Type: "int", Description: "Mail relay port. Default 587 for starttls, 465 for tls and 25 for none."},
	{Name: "SMTP_SECURITY", Type: "string", Default: "starttls", Description: "Connection protection: `starttls` (required upgrade before credentials or mail are sent), `tls` (implicit TLS) or `none` (clear text; requires SMTP_ALLOW_PLAINTEXT=true and APP_ENV=development). Certificate verification is never disabled."},
	{Name: "AUTOTASK_SYNC", Type: "bool", Default: "false", Description: "Synchronize tickets with Autotask (external references, push jobs, inbound updates). The REST client is implemented from the vendor documentation and unverified against a live service (health reports `unverified` until a call succeeded); it needs the AUTOTASK_* API user settings, otherwise pushes fail with a visible \"not configured\" state."},
	{Name: "AUTOTASK_API_USERNAME", Type: "string", Description: "Login name (email-style) of the Autotask API-only user; also used for the zone discovery. Part of the Autotask REST client; all of AUTOTASK_API_USERNAME, AUTOTASK_API_SECRET_FILE, AUTOTASK_INTEGRATION_CODE, AUTOTASK_COMPANY_ID, AUTOTASK_STATUS_MAP and AUTOTASK_PRIORITY_MAP are required once one is set."},
	{Name: "AUTOTASK_API_SECRET_FILE", Type: "string", Secret: true, Description: "Path to a file containing the Autotask API user's secret (mounted only into turaco-worker and turaco-api). The secret is sent only in the Secret header to the discovered Autotask zone host and is never stored or logged."},
	{Name: "AUTOTASK_INTEGRATION_CODE", Type: "string", Description: "The Autotask API tracking identifier (integration code) of the API user, sent in the ApiIntegrationCode header."},
	{Name: "AUTOTASK_COMPANY_ID", Type: "int", Description: "Numeric Autotask company (customer) id that receives the pushed tickets (required field companyID of the Tickets entity)."},
	{Name: "AUTOTASK_QUEUE_ID", Type: "int", Description: "Optional Autotask queue id for created tickets; required by Autotask when the ticket category demands a queue."},
	{Name: "AUTOTASK_STATUS_MAP", Type: "string", Description: "Maps every Turaco ticket status to the tenant's Autotask status picklist value, for example `new=1,open=1,in_progress=8,waiting=7,resolved=5,closed=5,cancelled=5` (the numbers are examples; picklists are tenant specific). All of new, open, in_progress, waiting, resolved, closed and cancelled are required."},
	{Name: "AUTOTASK_PRIORITY_MAP", Type: "string", Description: "Maps every Turaco priority to the tenant's Autotask priority picklist value, for example `low=3,normal=2,high=1,urgent=4` (examples). All of low, normal, high and urgent are required."},
	{Name: "AUTOTASK_RATE_LIMIT_PER_HOUR", Type: "int", Default: "3000", Description: "Turaco's own ceiling of Autotask requests per rolling hour (10 to 9000). Autotask allows 10,000 per database across all integrations; the remainder is left to them."},
	{Name: "INTUNE_SYNC", Type: "bool", Default: "false", Description: "Allow endpoint synchronization (POST /api/v1/endpoint-sync) from the Intune provider. The Microsoft Graph read client is implemented from the vendor documentation and unverified against a live tenant (health reports `unverified` until a call succeeded); it needs the MICROSOFT_GRAPH_* read registration, otherwise a run reports that the provider is not configured."},
	{Name: "INTUNE_GRAPH_BETA", Type: "bool", Default: "false", Description: "Let the Intune Graph read client call Graph beta endpoints for objects Microsoft documents only there: assignment filters, settings catalog policies and application install statuses (observations). Beta endpoints can change without notice. Off: filtered assignments are rejected by the ingestion and no observations are produced."},
	{Name: "MICROSOFT_GRAPH_TENANT_ID", Type: "string", Description: "Tenant GUID of the Microsoft Graph read app registration (client credentials, application permissions, read only) used by the Intune read synchronization. Setting any MICROSOFT_GRAPH_* key requires tenant id, client id and one credential."},
	{Name: "MICROSOFT_GRAPH_CLIENT_ID", Type: "string", Description: "Application (client) GUID of the Graph read app registration."},
	{Name: "MICROSOFT_GRAPH_CLIENT_SECRET_FILE", Type: "string", Secret: true, Description: "Path to a file containing the client secret of the Graph read registration (mounted only into turaco-api). Use either this or the certificate files; a certificate is preferred."},
	{Name: "MICROSOFT_GRAPH_CLIENT_SECRET_EXPIRES_AT", Type: "string", Description: "RFC 3339 expiry of the Graph read client secret, used only for the expiry health check."},
	{Name: "MICROSOFT_GRAPH_CLIENT_CERTIFICATE_FILE", Type: "string", Description: "PEM certificate registered on the Graph read app registration for client assertion. Requires MICROSOFT_GRAPH_CLIENT_PRIVATE_KEY_FILE."},
	{Name: "MICROSOFT_GRAPH_CLIENT_PRIVATE_KEY_FILE", Type: "string", Secret: true, Description: "PEM private key (RSA) of MICROSOFT_GRAPH_CLIENT_CERTIFICATE_FILE."},
	{Name: "MICROSOFT_GRAPH_WRITE_TENANT_ID", Type: "string", Description: "Tenant GUID of the separate Graph write app registration used only by the deployment engine in turaco-worker to assign Software Packages to Turaco-owned ring groups (needs SOFTWARE_DEPLOY_WRITE). Same rules as MICROSOFT_GRAPH_TENANT_ID."},
	{Name: "MICROSOFT_GRAPH_WRITE_CLIENT_ID", Type: "string", Description: "Application (client) GUID of the Graph write app registration."},
	{Name: "MICROSOFT_GRAPH_WRITE_CLIENT_SECRET_FILE", Type: "string", Secret: true, Description: "Path to a file containing the client secret of the Graph write registration (mounted only into turaco-worker). Use either this or the certificate files."},
	{Name: "MICROSOFT_GRAPH_WRITE_CLIENT_SECRET_EXPIRES_AT", Type: "string", Description: "RFC 3339 expiry of the Graph write client secret."},
	{Name: "MICROSOFT_GRAPH_WRITE_CLIENT_CERTIFICATE_FILE", Type: "string", Description: "PEM certificate registered on the Graph write app registration. Requires MICROSOFT_GRAPH_WRITE_CLIENT_PRIVATE_KEY_FILE."},
	{Name: "MICROSOFT_GRAPH_WRITE_CLIENT_PRIVATE_KEY_FILE", Type: "string", Secret: true, Description: "PEM private key (RSA) of MICROSOFT_GRAPH_WRITE_CLIENT_CERTIFICATE_FILE."},
	{Name: "SOFTWARE_PROVIDER_SYNC", Type: "bool", Default: "false", Description: "Allow the Software Package synchronization (POST /api/v1/software/packages/sync and the scheduled worker job) against the Software Management Provider (IntuneGet). The provider client is not implemented yet: with the switch on, a run reports that the provider is not configured."},
	{Name: "REMOTE_ACCESS_PROVIDERS", Type: "string", Description: "Comma-separated Remote Access Provider keys to enable (`rustdesk`, `anydesk`, `hoptodesk`; launch-link connectors, attended sessions only). Empty switches Remote Access off: no session can be requested. Unknown keys stop turaco-api and turaco-worker at startup. API and worker must use the same value."},
	{Name: "REMOTE_ACCESS_APPROVAL_REQUIRED_OWNERSHIP", Type: "string", Description: "Comma-separated Device ownerships (`corporate`, `personal`, `unknown`) whose Remote Access Sessions need a second approver holding remote_access.admin before they can be launched. Empty requires no approval."},
	{Name: "PRESENCE_ENABLED", Type: "bool", Default: "false", Description: "Startup gate of Workforce Presence (ADR-0028): routes, the retention job and the public availability contract. Off by default; enabling needs the customer's data protection impact assessment and works-council confirmation, recorded afterwards in the audited runtime setting presence.settings.enabled. API and worker must use the same value."},
	{Name: "AUDIT_RETENTION_DAYS", Type: "int", Default: "0", Description: "Audit log retention in days. 0 keeps every event. A value of 365 or more makes the worker purge older events in batches (job platform.audit.purge, itself audited as platform.audit.purged); smaller values are refused at startup and the database function never deletes events younger than 365 days. Legal retention is the operator's decision. API and worker should use the same value so the Audit page states the policy correctly."},
	{Name: "PRESENCE_RETENTION_DAYS", Type: "int", Default: "30", Description: "Upper bound of the Workforce Presence retention: past entries are deleted this many days after they ended; 1 to 30, never longer. The runtime setting can only shorten it."},
	{Name: "PRESENCE_SOURCE_STALE_AFTER", Type: "duration", Default: "24h", Description: "An externally sourced Workforce Presence signal older than this is shown as unknown instead of being trusted; at least 1m."},
	{Name: "AI_ENABLED", Type: "bool", Default: "false", Description: "Startup gate of Turaco AI (ADR-0029, F12): the assistant API, tools and provider calls. Off by default; when off only GET /api/v1/ai/status and the permission-protected configuration routes (settings, providers, usage) are mounted and no provider is called by users. Enabling also needs the audited runtime setting ai.settings.enabled and an enabled AI Provider. API and worker may differ (the worker only runs the cleanup jobs)."},
	{Name: "AI_SECRET_DIR", Type: "string", Description: "Directory with deployment secret files (for example mounted Docker secrets). An AI Provider's `secretRef` names a file in it (`<dir>/<secretRef>`, one line, read when the provider is built, never stored in the database, logged or sent into prompts). Empty: providers without credentials only (a local Ollama)."},
	{Name: "TENANT_ID", Type: "string", Default: "default", Description: "Fixed data plane (tenant) id of this installation (F12 A15, ADR-0007). It is attached to every authenticated principal on the server and written to AI audit entries and AI usage counters; it is never taken from request fields. 1 to 100 characters of letters, digits, `.`, `_` and `-`."},
	{Name: "SOFTWARE_DEPLOY_WRITE", Type: "bool", Default: "false", Description: "Capability: allow Deployments to write ring assignments to the Management Provider (start, resume, promote, resolving targets, clearing after a cancel or kill switch). The worker job endpoints.deployment_tick runs every minute either way; with the capability off it only runs the kill-switch sweep (pause, halt, queue clearing). API and worker must use the same value. Off by default; enabling is audited (endpoints.deploy_write.enabled). The Graph write client is implemented from the vendor documentation and unverified against a live tenant; without the MICROSOFT_GRAPH_WRITE_* registration the writer is a placeholder whose writes fail permanently, so a ring halts with assignment_failed."},
	{Name: "ADVISORY_SYNC", Type: "bool", Default: "false", Description: "Synchronize security advisories from the public NVD and CISA KEV feeds (scheduled worker job security.advisory_sync and `turaco-admin security sync-feeds`). No account is needed. Imported advisories start in status new; criteria come from the feed's CPE data and analysts decide applicability."},
	{Name: "ADVISORY_SOURCES", Type: "string", Default: "nvd,cisa_kev", Description: "Comma-separated advisory feeds to synchronize: `nvd` (NVD API 2.0) and/or `cisa_kev` (CISA Known Exploited Vulnerabilities catalog). Used when ADVISORY_SYNC is on and by the admin command."},
	{Name: "NVD_API_KEY_FILE", Type: "string", Secret: true, Description: "Path to a file containing an optional NVD API key (for example a Docker secret). With a key the NVD rate limit rises from 5 to 50 requests per 30 seconds. The key is sent only to the NVD API and never stored or logged."},
	{Name: "ADVISORY_SYNC_INTERVAL", Type: "duration", Default: "6h", Description: "How often the advisory sync job runs; at least 1h."},
	{Name: "SMTP_ALLOW_PLAINTEXT", Type: "bool", Default: "false", Description: "Allow SMTP_SECURITY=none (clear text, credentials included). Accepted only together with APP_ENV=development, for local test relays."},
	{Name: "SMTP_USERNAME", Type: "string", Description: "Relay account. Requires SMTP_PASSWORD_FILE."},
	{Name: "SMTP_PASSWORD_FILE", Type: "string", Secret: true, Description: "Path to a file containing the relay password (for example a Docker secret). Requires SMTP_USERNAME. The password is never stored in the database or logged."},
	{Name: "SMTP_CA_FILE", Type: "string", Description: "PEM file with CA certificates trusted for the relay, in addition to the system pool."},
	{Name: "SMTP_FROM", Type: "string", Description: "Sender address of notification emails, for example `Turaco <turaco@example.org>`. Required when SMTP_HOST is set."},
	{Name: "SMTP_TIMEOUT", Type: "duration", Default: "30s", Description: "Maximum duration of sending one email (connect, dialogue and transfer)."},
	{Name: "EMAIL_BASE_URL", Type: "string", Description: "Externally reachable address of the web application without a path, for example `https://turaco.example.org`; emails link to it. Required when SMTP_HOST is set; must be https outside development. Defaults to `http://localhost:5173` when APP_ENV=development; every other environment must set it explicitly."},
	{Name: "EMAIL_DEFAULT_LOCALE", Type: "string", Default: "en", Description: "Language of notification emails: `en` or `de` (recipients have no language setting yet)."},
	{Name: "AUTH_ENTRA_LOGIN_ENABLED", Type: "bool", Default: "false", Description: "Enable sign-in with Microsoft Entra ID (OpenID Connect authorization code flow with PKCE; GET /api/v1/auth/entra/start). Works in hosted and on-premises installations; needs outbound HTTPS to the Entra authority host (optionally through MICROSOFT_HTTP_PROXY). Users must be linked to an Entra identity (tenant id + object id) by an administrator; a link by email never happens. Can be combined with LDAP, Kerberos and local accounts."},
	{Name: "ENTRA_CLOUD", Type: "string", Default: "global", Description: "Microsoft cloud of the Entra tenant. Only `global` is built (EU tenants use the global cloud)."},
	{Name: "ENTRA_TENANT_MODE", Type: "string", Default: "single", Description: "`single` accepts exactly ENTRA_TENANT_ID; `multi_restricted` accepts the tenants listed in ENTRA_ALLOWED_TENANT_IDS (for a publisher-owned multi-tenant app registration). The Microsoft consumer tenant is always refused."},
	{Name: "ENTRA_TENANT_ID", Type: "string", Description: "Tenant (directory) GUID of the home Entra tenant. Required when ENTRA_TENANT_MODE=single."},
	{Name: "ENTRA_ALLOWED_TENANT_IDS", Type: "string", Description: "Comma-separated tenant GUIDs accepted when ENTRA_TENANT_MODE=multi_restricted."},
	{Name: "ENTRA_CLIENT_ID", Type: "string", Description: "Application (client) GUID of the Entra app registration used for sign-in. Required when AUTH_ENTRA_LOGIN_ENABLED=true."},
	{Name: "ENTRA_CLIENT_SECRET_FILE", Type: "string", Secret: true, Description: "Path to a file containing the client secret of the sign-in app registration (mounted only into turaco-api). Use either this or the certificate files; a certificate is preferred."},
	{Name: "ENTRA_CLIENT_SECRET_EXPIRES_AT", Type: "string", Description: "RFC 3339 expiry of the client secret, used only for the expiry health check (Entra does not expose it to the application)."},
	{Name: "ENTRA_CLIENT_CERTIFICATE_FILE", Type: "string", Description: "PEM certificate registered on the Entra app registration for client assertion. Requires ENTRA_CLIENT_PRIVATE_KEY_FILE."},
	{Name: "ENTRA_CLIENT_PRIVATE_KEY_FILE", Type: "string", Secret: true, Description: "PEM private key (RSA, PKCS#1 or PKCS#8) of ENTRA_CLIENT_CERTIFICATE_FILE; mounted only into turaco-api."},
	{Name: "ENTRA_REDIRECT_URL", Type: "string", Description: "Callback URL exactly as registered in Entra, ending in `/api/v1/auth/entra/callback`. It must be https (http only for localhost in development) and is never derived from request headers. The reverse proxy must forward the path unchanged."},
	{Name: "ENTRA_LINK_DIRECTORY_PROVIDER_KEY", Type: "string", Description: "Provider key of the synchronized on-premises directory (LDAP_PROVIDER_KEY) that ENTRA_TENANT_ID is bound to (Microsoft Entra Connect / Cloud Sync). When set, the sign-in also requests the delegated `User.Read` permission, reads the user's `onPremisesImmutableId` once from Microsoft Graph /me and links an Entra user to the directory-synchronized User with the same objectGUID (Active Directory only; the Graph call is made only for unlinked users of ENTRA_TENANT_ID with onPremisesSyncEnabled=true). Grant `User.Read` on the sign-in app registration. Empty disables the hybrid match."},
	{Name: "ENTRA_MAX_CLOCK_SKEW", Type: "duration", Default: "2m", Description: "Tolerated clock difference when validating exp, nbf and iat of the ID token; at most 5m."},
	{Name: "ENTRA_SESSION_MAX_AGE", Type: "duration", Default: "8h", Description: "Absolute lifetime of an Entra session (1h to 24h; one shift by default). The administration setting `auth.session_absolute_timeout` can lower it."},
	{Name: "MICROSOFT_HTTP_PROXY", Type: "string", Description: "Explicit HTTP(S) proxy (CONNECT) for all calls to Microsoft hosts. Environment proxy variables are not honoured implicitly."},
	{Name: "MICROSOFT_CA_FILE", Type: "string", Description: "PEM file with CA certificates trusted for Microsoft hosts in addition to the system pool (needed behind a TLS-inspecting proxy; an operator decision that weakens end-to-end TLS)."},
	{Name: "TEAMS_CHANNEL_DESTINATIONS_FILE", Type: "string", Description: "Path of a secret file with a JSON object of Teams destination key to Power Automate Workflows webhook URL (`{\"infrastructure\": \"https://prod-12.westeurope.logic.azure.com:443/workflows/...\"}`; hosts `*.logic.azure.com` and `*.api.powerplatform.com` only). The URLs are bearer secrets: mount the file read-only into turaco-worker (posts) and turaco-api (key list only); the UI and API show keys, never URLs. Empty means Teams channel posts are not configured. Needs outbound HTTPS (optionally through MICROSOFT_HTTP_PROXY). Enable the optional module `teams` and add channel routes under Administration after setting it."},
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

		SessionCookieSecure:  getenv("SESSION_COOKIE_SECURE", "true") != "false",
		AutotaskSync:         getenv("AUTOTASK_SYNC", "false") == "true",
		IntuneSync:           getenv("INTUNE_SYNC", "false") == "true",
		SoftwareProviderSync: getenv("SOFTWARE_PROVIDER_SYNC", "false") == "true",
		SoftwareDeployWrite:  getenv("SOFTWARE_DEPLOY_WRITE", "false") == "true",

		RemoteAccessProviders: splitList(os.Getenv("REMOTE_ACCESS_PROVIDERS")),
	}
	cfg.RemoteAccessApprovalOwnership = splitList(os.Getenv("REMOTE_ACCESS_APPROVAL_REQUIRED_OWNERSHIP"))
	for _, o := range cfg.RemoteAccessApprovalOwnership {
		if o != "corporate" && o != "personal" && o != "unknown" {
			return Config{}, fmt.Errorf("REMOTE_ACCESS_APPROVAL_REQUIRED_OWNERSHIP: %q is not corporate, personal or unknown", o)
		}
	}
	cfg.PresenceEnabled = getenv("PRESENCE_ENABLED", "false") == "true"
	if v := os.Getenv("AUDIT_RETENTION_DAYS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || (n != 0 && n < 365) || n > 36500 {
			return Config{}, fmt.Errorf("AUDIT_RETENTION_DAYS must be 0 (keep everything) or an integer from 365 to 36500")
		}
		cfg.AuditRetentionDays = n
	}
	cfg.PresenceRetentionDays = 30
	if v := os.Getenv("PRESENCE_RETENTION_DAYS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 30 {
			return Config{}, fmt.Errorf("PRESENCE_RETENTION_DAYS must be an integer from 1 to 30")
		}
		cfg.PresenceRetentionDays = n
	}
	var perr error
	if cfg.PresenceSourceStaleAfter, perr = getDuration("PRESENCE_SOURCE_STALE_AFTER", 24*time.Hour); perr != nil {
		return Config{}, perr
	}
	if cfg.PresenceSourceStaleAfter < time.Minute {
		return Config{}, fmt.Errorf("PRESENCE_SOURCE_STALE_AFTER must be at least 1m")
	}
	cfg.AIEnabled = getenv("AI_ENABLED", "false") == "true"
	cfg.AISecretDir = os.Getenv("AI_SECRET_DIR")
	cfg.TenantID = getenv("TENANT_ID", "default")
	if !tenantIDPattern.MatchString(cfg.TenantID) {
		return Config{}, fmt.Errorf("TENANT_ID must be 1 to 100 characters of letters, digits, '.', '_' and '-'")
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
	if cfg.PeopleLookupEnabled, err = getBool("PEOPLE_LOOKUP_ENABLED", true); err != nil {
		return Config{}, err
	}
	if cfg.AuthLocalLoginEnabled, err = getBool("AUTH_LOCAL_LOGIN_ENABLED", false); err != nil {
		return Config{}, err
	}
	if cfg.EmailBaseURL, err = LoadEmailBaseURL(cfg.Environment); err != nil {
		return Config{}, err
	}
	if cfg.AuthLocalLoginEnabled && cfg.EmailBaseURL == "" {
		return Config{}, fmt.Errorf("AUTH_LOCAL_LOGIN_ENABLED requires EMAIL_BASE_URL (invitation and reset links are built only from it)")
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

// splitList splits a comma-separated list, trims entries and drops empty ones.
func splitList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if part = strings.ToLower(strings.TrimSpace(part)); part != "" {
			out = append(out, part)
		}
	}
	return out
}
