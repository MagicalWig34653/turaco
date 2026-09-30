package config

import (
	"fmt"
	"os"
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
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	return cfg, nil
}

func getenv(name, fallback string) string {
	if value, ok := os.LookupEnv(name); ok && value != "" {
		return value
	}
	return fallback
}
