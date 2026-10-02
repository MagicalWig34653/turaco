package config

import (
	"strings"
	"testing"
	"time"
)

func setSMTPEnv(t *testing.T, overrides map[string]string) {
	t.Helper()
	env := map[string]string{
		"SMTP_HOST": "mail.example.org", "SMTP_PORT": "", "SMTP_SECURITY": "", "SMTP_USERNAME": "turaco",
		"SMTP_PASSWORD_FILE": "/run/secrets/smtp", "SMTP_CA_FILE": "", "SMTP_FROM": "Turaco <turaco@example.org>",
		"SMTP_TIMEOUT": "", "EMAIL_BASE_URL": "https://turaco.example.org", "EMAIL_DEFAULT_LOCALE": "",
	}
	for k, v := range overrides {
		env[k] = v
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
}

func TestSMTPDisabledWithoutHost(t *testing.T) {
	setSMTPEnv(t, map[string]string{"SMTP_HOST": "", "SMTP_FROM": "", "EMAIL_BASE_URL": ""})
	c, err := LoadSMTP("production")
	if err != nil || c.Enabled() {
		t.Fatalf("c=%+v err=%v, want disabled without error", c, err)
	}
}

func TestSMTPDefaults(t *testing.T) {
	setSMTPEnv(t, nil)
	c, err := LoadSMTP("production")
	if err != nil || !c.Enabled() {
		t.Fatal(err)
	}
	if c.Security != "starttls" || c.Port != 587 || c.Timeout != 30*time.Second || c.DefaultLocale != "en" || c.BaseURL != "https://turaco.example.org" {
		t.Errorf("defaults = %+v", c)
	}
}

func TestSMTPPortDefaultsFollowSecurity(t *testing.T) {
	for sec, port := range map[string]int{"starttls": 587, "tls": 465} {
		setSMTPEnv(t, map[string]string{"SMTP_SECURITY": sec})
		c, err := LoadSMTP("production")
		if err != nil || c.Port != port {
			t.Errorf("%s: port = %d %v, want %d", sec, c.Port, err, port)
		}
	}
	setSMTPEnv(t, map[string]string{"SMTP_SECURITY": "none", "SMTP_USERNAME": "", "SMTP_PASSWORD_FILE": "", "EMAIL_BASE_URL": "http://localhost:5173"})
	c, err := LoadSMTP("development")
	if err != nil || c.Port != 25 {
		t.Errorf("none: %+v %v", c, err)
	}
}

func TestSMTPRejectsUnsafeOrInvalidSettings(t *testing.T) {
	cases := map[string]struct {
		env map[string]string
		in  string
	}{
		"clear text in production":  {map[string]string{"SMTP_SECURITY": "none"}, "production"},
		"unknown security":          {map[string]string{"SMTP_SECURITY": "ssl"}, "production"},
		"host with scheme":          {map[string]string{"SMTP_HOST": "smtp://mail.example.org"}, "production"},
		"host with port":            {map[string]string{"SMTP_HOST": "mail.example.org:25"}, "production"},
		"bad port":                  {map[string]string{"SMTP_PORT": "70000"}, "production"},
		"user without password":     {map[string]string{"SMTP_PASSWORD_FILE": ""}, "production"},
		"password without user":     {map[string]string{"SMTP_USERNAME": ""}, "production"},
		"missing from":              {map[string]string{"SMTP_FROM": ""}, "production"},
		"invalid from":              {map[string]string{"SMTP_FROM": "nobody"}, "production"},
		"from with newline":         {map[string]string{"SMTP_FROM": "a@example.org\nBcc: b@example.org"}, "production"},
		"missing base url":          {map[string]string{"EMAIL_BASE_URL": ""}, "production"},
		"base url with path":        {map[string]string{"EMAIL_BASE_URL": "https://turaco.example.org/app"}, "production"},
		"base url with credentials": {map[string]string{"EMAIL_BASE_URL": "https://u:p@turaco.example.org"}, "production"},
		"http base url in prod":     {map[string]string{"EMAIL_BASE_URL": "http://turaco.example.org"}, "production"},
		"bad locale":                {map[string]string{"EMAIL_DEFAULT_LOCALE": "fr"}, "production"},
		"bad timeout":               {map[string]string{"SMTP_TIMEOUT": "soon"}, "production"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			setSMTPEnv(t, c.env)
			if _, err := LoadSMTP(c.in); err == nil {
				t.Fatalf("LoadSMTP accepted %v", c.env)
			}
		})
	}
}

func TestSMTPErrorsNeverContainSecrets(t *testing.T) {
	setSMTPEnv(t, map[string]string{"SMTP_PORT": "x", "SMTP_PASSWORD_FILE": "/run/secrets/very-secret-path"})
	_, err := LoadSMTP("production")
	if err == nil || strings.Contains(err.Error(), "very-secret-path") {
		t.Fatalf("err = %v", err)
	}
}

func TestSMTPPasswordFileIsASecretSetting(t *testing.T) {
	for _, d := range Registry {
		if d.Name == "SMTP_PASSWORD_FILE" && !d.Secret {
			t.Fatal("SMTP_PASSWORD_FILE must be documented as a secret")
		}
	}
}
