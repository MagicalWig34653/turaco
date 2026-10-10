package config

import (
	"fmt"
	"net/mail"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// SMTPConfig configures the email channel of the Notification service. It is
// loaded by turaco-worker, which sends the mail; email is disabled unless
// SMTP_HOST is set. The relay password is never a configuration value: only
// the path of a file holding it (a deployment secret) is configured.
type SMTPConfig struct {
	Host string
	Port int
	// Security is "starttls" (default), "tls" or "none" (development only).
	Security     string
	Username     string
	PasswordFile string
	CAFile       string
	From         string
	Timeout      time.Duration
	// BaseURL is the externally reachable address of the web application
	// (scheme and host, no path); links in emails start with it.
	BaseURL string
	// DefaultLocale is the language of emails ("en" or "de").
	DefaultLocale string
}

// DevEmailBaseURL is the EMAIL_BASE_URL default of APP_ENV=development only: the Vite dev server. Every other
// environment must set EMAIL_BASE_URL explicitly.
const DevEmailBaseURL = "http://localhost:5173"

// LoadEmailBaseURL validates EMAIL_BASE_URL and returns it without a trailing slash; "" when it is unset outside
// development (in development it defaults to DevEmailBaseURL).
func LoadEmailBaseURL(environment string) (string, error) {
	base := strings.TrimRight(os.Getenv("EMAIL_BASE_URL"), "/")
	if base == "" && environment == "development" {
		base = DevEmailBaseURL
	}
	if base == "" {
		return "", nil
	}
	u, perr := url.Parse(base)
	switch {
	case perr != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil:
		return "", fmt.Errorf("EMAIL_BASE_URL must be the web application's address, for example https://turaco.example.org")
	case u.Scheme != "https" && !(u.Scheme == "http" && environment == "development"):
		return "", fmt.Errorf("EMAIL_BASE_URL must use https (http is accepted only with APP_ENV=development)")
	}
	return base, nil
}

// Enabled reports whether email notifications are configured.
func (c SMTPConfig) Enabled() bool { return c.Host != "" }

// LoadSMTP loads and validates the email configuration. Fail closed: a
// clear-text relay needs an explicit development environment.
func LoadSMTP(environment string) (SMTPConfig, error) {
	c := SMTPConfig{Host: strings.TrimSpace(os.Getenv("SMTP_HOST"))}
	if c.Host == "" {
		return c, nil
	}
	if strings.ContainsAny(c.Host, " \t\r\n/@:") {
		return SMTPConfig{}, fmt.Errorf("SMTP_HOST must be a host name without scheme, port or credentials")
	}
	c.Security = strings.ToLower(getenv("SMTP_SECURITY", "starttls"))
	defaultPort := "587"
	switch c.Security {
	case "starttls":
	case "tls":
		defaultPort = "465"
	case "none":
		// Fail closed: APP_ENV defaults to development, so clear text also needs
		// an explicit opt-in that a production deployment never sets by accident.
		allow, err := getBool("SMTP_ALLOW_PLAINTEXT", false)
		if err != nil {
			return SMTPConfig{}, err
		}
		if environment != "development" || !allow {
			return SMTPConfig{}, fmt.Errorf("SMTP_SECURITY=none requires SMTP_ALLOW_PLAINTEXT=true and APP_ENV=development")
		}
		defaultPort = "25"
	default:
		return SMTPConfig{}, fmt.Errorf("SMTP_SECURITY must be starttls, tls or none")
	}
	var err error
	if c.Port, err = strconv.Atoi(getenv("SMTP_PORT", defaultPort)); err != nil || c.Port < 1 || c.Port > 65535 {
		return SMTPConfig{}, fmt.Errorf("SMTP_PORT must be an integer between 1 and 65535")
	}
	c.Username = os.Getenv("SMTP_USERNAME")
	c.PasswordFile = os.Getenv("SMTP_PASSWORD_FILE")
	if (c.Username == "") != (c.PasswordFile == "") {
		return SMTPConfig{}, fmt.Errorf("SMTP_USERNAME and SMTP_PASSWORD_FILE must be set together")
	}
	c.CAFile = os.Getenv("SMTP_CA_FILE")
	c.From = os.Getenv("SMTP_FROM")
	if _, err := mail.ParseAddress(c.From); err != nil || strings.ContainsAny(c.From, "\r\n") {
		return SMTPConfig{}, fmt.Errorf("SMTP_FROM must be a valid email address when SMTP_HOST is set")
	}
	if c.Timeout, err = getDuration("SMTP_TIMEOUT", 30*time.Second); err != nil {
		return SMTPConfig{}, err
	}
	c.BaseURL = strings.TrimRight(os.Getenv("EMAIL_BASE_URL"), "/")
	if c.BaseURL == "" && environment == "development" {
		c.BaseURL = DevEmailBaseURL
	}
	u, perr := url.Parse(c.BaseURL)
	switch {
	case c.BaseURL == "" || perr != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil:
		return SMTPConfig{}, fmt.Errorf("EMAIL_BASE_URL must be the web application's address, for example https://turaco.example.org")
	case u.Scheme != "https" && !(u.Scheme == "http" && environment == "development"):
		return SMTPConfig{}, fmt.Errorf("EMAIL_BASE_URL must use https (http is accepted only with APP_ENV=development)")
	}
	c.DefaultLocale = strings.ToLower(getenv("EMAIL_DEFAULT_LOCALE", "en"))
	if c.DefaultLocale != "en" && c.DefaultLocale != "de" {
		return SMTPConfig{}, fmt.Errorf("EMAIL_DEFAULT_LOCALE must be en or de")
	}
	return c, nil
}
