package config

import (
	"fmt"
	"os"
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
}

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
	return cfg, nil
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

func getenv(name, fallback string) string {
	if value, ok := os.LookupEnv(name); ok && value != "" {
		return value
	}
	return fallback
}
