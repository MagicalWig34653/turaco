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
		"LDAP_URL": "ldaps://dc.example.test", "LDAP_PROVIDER_KEY": "", "LDAP_START_TLS": "",
		"LDAP_BIND_DN": "CN=svc,DC=example,DC=test", "LDAP_BIND_PASSWORD_FILE": "/run/secrets/ldap",
		"LDAP_DIRECTORY_TYPE": "", "LDAP_USER_BASE_DN": "OU=Users,DC=example,DC=test",
		"LDAP_GROUP_BASE_DN": "OU=Groups,DC=example,DC=test", "LDAP_USER_FILTER": "", "LDAP_GROUP_FILTER": "",
		"LDAP_SYNC_INTERVAL": "", "LDAP_SYNC_TIMEOUT": "", "LDAP_SYNC_MAX_DEACTIVATION_PERCENT": "",
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
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LDAP.Enabled() {
		t.Fatal("LDAP must be disabled without LDAP_URL")
	}
}

func TestLoadLDAPDefaults(t *testing.T) {
	setLDAPEnv(t, nil)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	l := cfg.LDAP
	if l.ProviderKey != "ad" || l.DirectoryType != DirectoryTypeActiveDirectory || l.SyncInterval != time.Hour ||
		l.SyncTimeout != 15*time.Minute || l.MaxDeactivationPercent != 10 ||
		l.UserFilter != "(&(objectCategory=person)(objectClass=user))" || l.GroupFilter != "(objectClass=group)" {
		t.Fatalf("unexpected LDAP defaults %+v", l)
	}
}

func TestLoadLDAPOpenLDAPFilters(t *testing.T) {
	setLDAPEnv(t, map[string]string{"LDAP_DIRECTORY_TYPE": "openldap"})
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LDAP.UserFilter != "(objectClass=inetOrgPerson)" || cfg.LDAP.GroupFilter != "(objectClass=groupOfNames)" {
		t.Fatalf("unexpected filters %+v", cfg.LDAP)
	}
}

func TestLoadLDAPValidation(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"plain ldap in production", map[string]string{"LDAP_URL": "ldap://dc.example.test"}},
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
		{"percent out of range", map[string]string{"LDAP_SYNC_MAX_DEACTIVATION_PERCENT": "101"}},
		{"percent not a number", map[string]string{"LDAP_SYNC_MAX_DEACTIVATION_PERCENT": "ten"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setLDAPEnv(t, tt.env)
			if _, err := Load(); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestLoadLDAPPlainAllowedInDevelopment(t *testing.T) {
	setLDAPEnv(t, map[string]string{"LDAP_URL": "ldap://localhost:389"})
	t.Setenv("APP_ENV", "development")
	if _, err := Load(); err != nil {
		t.Fatalf("plain LDAP must be allowed in development: %v", err)
	}
}
