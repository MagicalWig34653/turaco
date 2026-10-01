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
