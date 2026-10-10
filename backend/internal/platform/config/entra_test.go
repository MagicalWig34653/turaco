package config

import (
	"strings"
	"testing"
	"time"
)

const (
	testTenant = "11111111-2222-3333-4444-555555555555"
	testClient = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
)

func entraEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range []string{"AUTH_ENTRA_LOGIN_ENABLED", "ENTRA_CLOUD", "ENTRA_TENANT_MODE", "ENTRA_TENANT_ID", "ENTRA_ALLOWED_TENANT_IDS", "ENTRA_CLIENT_ID",
		"ENTRA_CLIENT_SECRET_FILE", "ENTRA_CLIENT_SECRET_EXPIRES_AT", "ENTRA_CLIENT_CERTIFICATE_FILE", "ENTRA_CLIENT_PRIVATE_KEY_FILE", "ENTRA_REDIRECT_URL",
		"ENTRA_MAX_CLOCK_SKEW", "ENTRA_SESSION_MAX_AGE", "MICROSOFT_HTTP_PROXY", "MICROSOFT_CA_FILE", "APP_ENV"} {
		t.Setenv(k, "")
	}
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func validEntra() map[string]string {
	return map[string]string{
		"AUTH_ENTRA_LOGIN_ENABLED": "true", "ENTRA_TENANT_ID": testTenant, "ENTRA_CLIENT_ID": testClient,
		"ENTRA_CLIENT_SECRET_FILE": "/run/secrets/entra", "ENTRA_REDIRECT_URL": "https://turaco.example.org/api/v1/auth/entra/callback", "APP_ENV": "production",
	}
}

func TestLoadEntraDisabledValidatesNothing(t *testing.T) {
	entraEnv(t, map[string]string{"ENTRA_CLIENT_ID": "garbage"})
	c, err := LoadEntra()
	if err != nil || c.Enabled {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestLoadEntraValid(t *testing.T) {
	entraEnv(t, validEntra())
	c, err := LoadEntra()
	if err != nil || !c.Enabled || c.TenantMode != EntraTenantSingle || c.SessionMaxAge != 8*time.Hour || c.MaxClockSkew != 2*time.Minute || len(c.AllowedTenantIDs) != 1 {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestLoadEntraRefusals(t *testing.T) {
	cases := map[string]struct {
		patch map[string]string
		want  string
	}{
		"sovereign cloud":            {map[string]string{"ENTRA_CLOUD": "china"}, "not supported"},
		"bad mode":                   {map[string]string{"ENTRA_TENANT_MODE": "any"}, "ENTRA_TENANT_MODE"},
		"single needs tenant":        {map[string]string{"ENTRA_TENANT_ID": "common"}, "ENTRA_TENANT_ID"},
		"consumer tenant":            {map[string]string{"ENTRA_TENANT_MODE": "multi_restricted", "ENTRA_ALLOWED_TENANT_IDS": "9188040d-6c67-4c5b-b112-36a304b66dad"}, "consumer"},
		"multi needs list":           {map[string]string{"ENTRA_TENANT_MODE": "multi_restricted"}, "ENTRA_ALLOWED_TENANT_IDS"},
		"client id":                  {map[string]string{"ENTRA_CLIENT_ID": "x"}, "ENTRA_CLIENT_ID"},
		"secret and cert":            {map[string]string{"ENTRA_CLIENT_CERTIFICATE_FILE": "/c", "ENTRA_CLIENT_PRIVATE_KEY_FILE": "/k"}, "not both"},
		"no credential":              {map[string]string{"ENTRA_CLIENT_SECRET_FILE": ""}, "required"},
		"cert without key":           {map[string]string{"ENTRA_CLIENT_SECRET_FILE": "", "ENTRA_CLIENT_CERTIFICATE_FILE": "/c"}, "together"},
		"http redirect in prod":      {map[string]string{"ENTRA_REDIRECT_URL": "http://turaco.example.org/api/v1/auth/entra/callback"}, "https"},
		"redirect path":              {map[string]string{"ENTRA_REDIRECT_URL": "https://turaco.example.org/cb"}, "callback"},
		"redirect query":             {map[string]string{"ENTRA_REDIRECT_URL": "https://turaco.example.org/api/v1/auth/entra/callback?x=1"}, "callback URL"},
		"skew too large":             {map[string]string{"ENTRA_MAX_CLOCK_SKEW": "10m"}, "ENTRA_MAX_CLOCK_SKEW"},
		"session too short":          {map[string]string{"ENTRA_SESSION_MAX_AGE": "10m"}, "ENTRA_SESSION_MAX_AGE"},
		"bad proxy":                  {map[string]string{"MICROSOFT_HTTP_PROXY": "socks5://p"}, "MICROSOFT_HTTP_PROXY"},
		"bad link key":               {map[string]string{"ENTRA_LINK_DIRECTORY_PROVIDER_KEY": "AD Corp"}, "ENTRA_LINK_DIRECTORY_PROVIDER_KEY"},
		"link key needs home tenant": {map[string]string{"ENTRA_LINK_DIRECTORY_PROVIDER_KEY": "ad", "ENTRA_TENANT_MODE": "multi_restricted", "ENTRA_TENANT_ID": "", "ENTRA_ALLOWED_TENANT_IDS": testTenant}, "ENTRA_TENANT_ID is required"},
		"bad secret expiry":          {map[string]string{"ENTRA_CLIENT_SECRET_EXPIRES_AT": "tomorrow"}, "RFC 3339"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env := validEntra()
			for k, v := range tc.patch {
				env[k] = v
			}
			entraEnv(t, env)
			_, err := LoadEntra()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestLoadEntraHTTPRedirectOnlyForLocalhostInDevelopment(t *testing.T) {
	env := validEntra()
	env["APP_ENV"] = "development"
	env["ENTRA_REDIRECT_URL"] = "http://localhost:8080/api/v1/auth/entra/callback"
	entraEnv(t, env)
	if _, err := LoadEntra(); err != nil {
		t.Fatal(err)
	}
	env["ENTRA_REDIRECT_URL"] = "http://turaco.internal/api/v1/auth/entra/callback"
	entraEnv(t, env)
	if _, err := LoadEntra(); err == nil {
		t.Fatal("http to a non-local host must be refused even in development")
	}
}

func TestLoadEntraLinkDirectoryProviderKey(t *testing.T) {
	env := validEntra()
	entraEnv(t, env)
	if c, err := LoadEntra(); err != nil || c.LinkDirectoryProviderKey != "" {
		t.Fatalf("default: %+v %v", c, err)
	}
	env["ENTRA_LINK_DIRECTORY_PROVIDER_KEY"] = "ad"
	entraEnv(t, env)
	if c, err := LoadEntra(); err != nil || c.LinkDirectoryProviderKey != "ad" {
		t.Fatalf("configured: %+v %v", c, err)
	}
}
