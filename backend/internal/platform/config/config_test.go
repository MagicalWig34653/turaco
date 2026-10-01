package config

import (
	"testing"
	"time"
)

func TestLoadRequiresDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected DATABASE_URL validation error")
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("S3_PATH_STYLE", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Fatalf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if !cfg.S3PathStyle {
		t.Fatal("S3PathStyle should default to true")
	}
}

func TestLoadSessionDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	// Empty means unset for Load; isolates the test from a sourced .env.
	for _, name := range []string{"SESSION_IDLE_TIMEOUT", "SESSION_ABSOLUTE_TIMEOUT", "SESSION_COOKIE_SECURE"} {
		t.Setenv(name, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SessionIdleTimeout != 8*time.Hour || cfg.SessionAbsoluteTimeout != 24*time.Hour {
		t.Fatalf("timeouts = %v/%v", cfg.SessionIdleTimeout, cfg.SessionAbsoluteTimeout)
	}
	if !cfg.SessionCookieSecure {
		t.Fatal("SessionCookieSecure must default to true")
	}
}

func TestLoadSessionOverrides(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("SESSION_IDLE_TIMEOUT", "30m")
	t.Setenv("SESSION_ABSOLUTE_TIMEOUT", "2h")
	t.Setenv("SESSION_COOKIE_SECURE", "false")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SessionIdleTimeout != 30*time.Minute || cfg.SessionAbsoluteTimeout != 2*time.Hour || cfg.SessionCookieSecure {
		t.Fatalf("unexpected config %+v", cfg)
	}
}

func TestLoadSessionValidation(t *testing.T) {
	tests := []struct{ name, idle, abs string }{
		{"unparseable idle", "soon", ""},
		{"unparseable absolute", "", "later"},
		{"zero idle", "0s", ""},
		{"negative absolute", "", "-1h"},
		{"idle exceeds absolute", "10h", "5h"},
		{"default absolute smaller than idle", "48h", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://example")
			t.Setenv("SESSION_IDLE_TIMEOUT", tt.idle)
			t.Setenv("SESSION_ABSOLUTE_TIMEOUT", tt.abs)
			if _, err := Load(); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func setLDAPEnv(t *testing.T, overrides map[string]string) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("APP_ENV", "production")
	values := map[string]string{
		"LDAP_URL": "ldaps://dc.example.test", "LDAP_PROVIDER_KEY": "", "LDAP_START_TLS": "", "LDAP_ALLOW_PLAINTEXT": "",
		"LDAP_BIND_DN": "CN=svc,DC=example,DC=test", "LDAP_BIND_PASSWORD_FILE": "/run/secrets/ldap",
		"LDAP_DIRECTORY_TYPE": "", "LDAP_USER_BASE_DN": "OU=Users,DC=example,DC=test",
		"LDAP_GROUP_BASE_DN": "OU=Groups,DC=example,DC=test", "LDAP_USER_FILTER": "", "LDAP_GROUP_FILTER": "",
		"LDAP_SYNC_INTERVAL": "", "LDAP_SYNC_TIMEOUT": "", "LDAP_SYNC_MAX_MISSING_PERCENT": "",
	}
	for k, v := range overrides {
		values[k] = v
	}
	for k, v := range values {
		t.Setenv(k, v)
	}
}

func TestLoadLDAPDisabledByDefault(t *testing.T) {
	setLDAPEnv(t, map[string]string{"LDAP_URL": ""})
	l, err := LoadLDAP("production")
	if err != nil {
		t.Fatal(err)
	}
	if l.Enabled() {
		t.Fatal("LDAP must be disabled without LDAP_URL")
	}
	cfg, err := Load()
	if err != nil || cfg.DirectoryProviderKey != "" {
		t.Fatalf("DirectoryProviderKey = %q, %v; want empty", cfg.DirectoryProviderKey, err)
	}
}

// The API only needs the provider key; worker-only settings such as the bind
// password file must not be required by Load.
func TestLoadDirectoryProviderKeyWithoutWorkerSettings(t *testing.T) {
	setLDAPEnv(t, map[string]string{"LDAP_BIND_DN": "", "LDAP_BIND_PASSWORD_FILE": "", "LDAP_USER_BASE_DN": "", "LDAP_GROUP_BASE_DN": "", "LDAP_PROVIDER_KEY": "corp"})
	cfg, err := Load()
	if err != nil || cfg.DirectoryProviderKey != "corp" {
		t.Fatalf("DirectoryProviderKey = %q, %v; want corp", cfg.DirectoryProviderKey, err)
	}
	setLDAPEnv(t, map[string]string{"LDAP_PROVIDER_KEY": "Not Valid"})
	if _, err := Load(); err == nil {
		t.Fatal("expected invalid provider key error")
	}
}

func TestLoadLDAPDefaults(t *testing.T) {
	setLDAPEnv(t, nil)
	l, err := LoadLDAP("production")
	if err != nil {
		t.Fatal(err)
	}
	if l.ProviderKey != "ad" || l.DirectoryType != DirectoryTypeActiveDirectory || l.SyncInterval != time.Hour ||
		l.SyncTimeout != 15*time.Minute || l.MaxMissingPercent != 10 ||
		l.UserFilter != "(&(objectCategory=person)(objectClass=user))" || l.GroupFilter != "(objectClass=group)" {
		t.Fatalf("unexpected LDAP defaults %+v", l)
	}
}

func TestLoadLDAPOpenLDAPFilters(t *testing.T) {
	setLDAPEnv(t, map[string]string{"LDAP_DIRECTORY_TYPE": "openldap"})
	l, err := LoadLDAP("production")
	if err != nil {
		t.Fatal(err)
	}
	if l.UserFilter != "(objectClass=inetOrgPerson)" || l.GroupFilter != "(objectClass=groupOfNames)" {
		t.Fatalf("unexpected filters %+v", l)
	}
}

func TestLoadLDAPValidation(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"plain ldap in production", map[string]string{"LDAP_URL": "ldap://dc.example.test"}},
		{"plain ldap opt-in outside development", map[string]string{"LDAP_URL": "ldap://dc.example.test", "LDAP_ALLOW_PLAINTEXT": "true"}},
		{"starttls not a boolean", map[string]string{"LDAP_URL": "ldap://dc.example.test", "LDAP_START_TLS": "yes"}},
		{"allow plaintext not a boolean", map[string]string{"LDAP_ALLOW_PLAINTEXT": "sure"}},
		{"starttls with ldaps", map[string]string{"LDAP_START_TLS": "true"}},
		{"unknown scheme", map[string]string{"LDAP_URL": "https://dc.example.test"}},
		{"missing host", map[string]string{"LDAP_URL": "ldaps://"}},
		{"missing bind dn", map[string]string{"LDAP_BIND_DN": ""}},
		{"missing password file", map[string]string{"LDAP_BIND_PASSWORD_FILE": ""}},
		{"missing user base", map[string]string{"LDAP_USER_BASE_DN": ""}},
		{"missing group base", map[string]string{"LDAP_GROUP_BASE_DN": ""}},
		{"bad provider key", map[string]string{"LDAP_PROVIDER_KEY": "AD Main"}},
		{"unknown directory type", map[string]string{"LDAP_DIRECTORY_TYPE": "novell"}},
		{"interval too short", map[string]string{"LDAP_SYNC_INTERVAL": "1m"}},
		{"percent out of range", map[string]string{"LDAP_SYNC_MAX_MISSING_PERCENT": "101"}},
		{"percent not a number", map[string]string{"LDAP_SYNC_MAX_MISSING_PERCENT": "ten"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setLDAPEnv(t, tt.env)
			if _, err := LoadLDAP("production"); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestLoadLDAPPlainAllowedInDevelopment(t *testing.T) {
	setLDAPEnv(t, map[string]string{"LDAP_URL": "ldap://localhost:389"})
	if _, err := LoadLDAP("development"); err == nil {
		t.Fatal("plain LDAP must need LDAP_ALLOW_PLAINTEXT even in development")
	}
	t.Setenv("LDAP_ALLOW_PLAINTEXT", "true")
	if l, err := LoadLDAP("development"); err != nil || !l.AllowPlaintext {
		t.Fatalf("plain LDAP with opt-in must be allowed in development: %+v, %v", l, err)
	}
	t.Setenv("LDAP_START_TLS", "1")
	if l, err := LoadLDAP("production"); err != nil || !l.StartTLS {
		t.Fatalf("LDAP_START_TLS=1 must enable StartTLS: %+v, %v", l, err)
	}
}

func TestLoadLDAPConnectionNeedsNoBindSettings(t *testing.T) {
	setLDAPEnv(t, map[string]string{"LDAP_BIND_DN": "", "LDAP_BIND_PASSWORD_FILE": "", "LDAP_USER_BASE_DN": "", "LDAP_GROUP_BASE_DN": ""})
	c, err := LoadLDAPConnection("production")
	if err != nil || !c.Enabled() || c.BindDN != "" || c.BindPasswordFile != "" {
		t.Fatalf("LoadLDAPConnection = %+v, %v", c, err)
	}
	if _, err := LoadLDAP("production"); err == nil {
		t.Fatal("LoadLDAP must still require bind settings")
	}
	setLDAPEnv(t, map[string]string{"LDAP_URL": "ldap://dc.example.test"})
	if _, err := LoadLDAPConnection("production"); err == nil {
		t.Fatal("connection loading must enforce the TLS policy too")
	}
}

func TestLoadAuthSettings(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("AUTH_EMERGENCY_LOGIN_ENABLED", "")
	t.Setenv("HTTP_TRUSTED_PROXIES", "")
	cfg, err := Load()
	if err != nil || cfg.AuthEmergencyLoginEnabled || len(cfg.TrustedProxies) != 0 {
		t.Fatalf("defaults = %+v, %v", cfg, err)
	}
	t.Setenv("AUTH_EMERGENCY_LOGIN_ENABLED", "true")
	t.Setenv("HTTP_TRUSTED_PROXIES", "10.0.0.0/8, 192.168.1.7 ,fd00::/8")
	cfg, err = Load()
	if err != nil || !cfg.AuthEmergencyLoginEnabled || len(cfg.TrustedProxies) != 3 ||
		cfg.TrustedProxies[1].String() != "192.168.1.7/32" {
		t.Fatalf("parsed = %+v, %v", cfg.TrustedProxies, err)
	}
	for _, bad := range []string{"10.0.0.0/33", "not-an-ip"} {
		t.Setenv("HTTP_TRUSTED_PROXIES", bad)
		if _, err := Load(); err == nil {
			t.Fatalf("HTTP_TRUSTED_PROXIES=%q must be rejected", bad)
		}
	}
	t.Setenv("HTTP_TRUSTED_PROXIES", "")
	t.Setenv("AUTH_EMERGENCY_LOGIN_ENABLED", "maybe")
	if _, err := Load(); err == nil {
		t.Fatal("invalid boolean must be rejected")
	}
}
