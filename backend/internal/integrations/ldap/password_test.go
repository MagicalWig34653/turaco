package ldap

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	goldap "github.com/go-ldap/ldap/v3"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authentication"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
)

const (
	verifyDN       = "CN=Alice,OU=Users,DC=example,DC=test"
	verifyPassword = "Tr0ub4dor&3-correct-horse"
)

func newTestVerifier(t *testing.T, cfg config.LDAPConfig, f *fakeDirectory) *PasswordVerifier {
	t.Helper()
	v, err := NewPasswordVerifier(cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	v.conn.dial = f.dial
	return v
}

func TestVerifyPasswordSuccessBindsAsDN(t *testing.T) {
	f := newFakeDirectory()
	v := newTestVerifier(t, adConfig(), f)
	if err := v.VerifyPassword(context.Background(), verifyDN, verifyPassword); err != nil {
		t.Fatal(err)
	}
	if f.boundUser != verifyDN || f.boundPass != verifyPassword {
		t.Fatalf("bound %q", f.boundUser)
	}
	if f.startTLS != 0 {
		t.Fatal("ldaps must not use StartTLS")
	}
	if f.dials != 1 || f.closeCount() != 1 {
		t.Fatalf("dials=%d closes=%d", f.dials, f.closeCount())
	}
	if len(f.snapshotSearches()) != 0 {
		t.Fatal("password verification must not search")
	}
}

func TestVerifyPasswordNewConnectionPerCall(t *testing.T) {
	f := newFakeDirectory()
	v := newTestVerifier(t, adConfig(), f)
	for i := 0; i < 3; i++ {
		if err := v.VerifyPassword(context.Background(), verifyDN, verifyPassword); err != nil {
			t.Fatal(err)
		}
	}
	if f.dials != 3 || f.closeCount() != 3 {
		t.Fatalf("dials=%d closes=%d", f.dials, f.closeCount())
	}
}

func TestVerifyPasswordEmptyPasswordNeverTouchesNetwork(t *testing.T) {
	f := newFakeDirectory()
	v := newTestVerifier(t, adConfig(), f)
	err := v.VerifyPassword(context.Background(), verifyDN, "")
	if !errors.Is(err, authentication.ErrInvalidCredentials) {
		t.Fatalf("err = %v", err)
	}
	err = v.VerifyPassword(context.Background(), "", verifyPassword)
	if !errors.Is(err, authentication.ErrInvalidCredentials) {
		t.Fatalf("err = %v", err)
	}
	if f.dials != 0 {
		t.Fatalf("dials = %d, want 0", f.dials)
	}
}

func TestVerifyPasswordInvalidCredentials(t *testing.T) {
	f := newFakeDirectory()
	f.bindErr = goldap.NewError(goldap.LDAPResultInvalidCredentials, errors.New("80090308: data 52e, password "+verifyPassword))
	v := newTestVerifier(t, adConfig(), f)
	err := v.VerifyPassword(context.Background(), verifyDN, verifyPassword)
	if !errors.Is(err, authentication.ErrInvalidCredentials) {
		t.Fatalf("err = %v", err)
	}
	if errors.Is(err, authentication.ErrProviderUnavailable) || strings.Contains(err.Error(), verifyPassword) {
		t.Fatalf("err leaks or misclassifies: %v", err)
	}
	if f.closeCount() != 1 {
		t.Fatal("connection not closed")
	}
}

func TestVerifyPasswordProviderUnavailable(t *testing.T) {
	secretErr := errors.New("dial tcp 10.0.0.1:636: " + verifyPassword)
	tests := []struct {
		name string
		set  func(f *fakeDirectory)
	}{
		{"dial", func(f *fakeDirectory) { f.dialErr = secretErr }},
		{"other result code", func(f *fakeDirectory) {
			f.bindErr = goldap.NewError(goldap.LDAPResultUnwillingToPerform, errors.New(verifyDN))
		}},
		{"network error on bind", func(f *fakeDirectory) { f.bindErr = secretErr }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeDirectory()
			tt.set(f)
			v := newTestVerifier(t, adConfig(), f)
			err := v.VerifyPassword(context.Background(), verifyDN, verifyPassword)
			if !errors.Is(err, authentication.ErrProviderUnavailable) || errors.Is(err, authentication.ErrInvalidCredentials) {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), verifyPassword) || strings.Contains(err.Error(), "10.0.0.1") {
				t.Fatalf("error leaks details: %v", err)
			}
		})
	}
}

func TestVerifyPasswordStartTLS(t *testing.T) {
	cfg := adConfig()
	cfg.URL = "ldap://dc.example.test:389"
	cfg.StartTLS = true
	f := newFakeDirectory()
	v := newTestVerifier(t, cfg, f)
	if err := v.VerifyPassword(context.Background(), verifyDN, verifyPassword); err != nil {
		t.Fatal(err)
	}
	if f.startTLS != 1 || f.startTLSCfg == nil || f.startTLSCfg.ServerName != "dc.example.test" {
		t.Fatalf("startTLS=%d cfg=%+v", f.startTLS, f.startTLSCfg)
	}

	f2 := newFakeDirectory()
	f2.startTLSErr = errors.New("tls: handshake failure " + verifyPassword)
	v2 := newTestVerifier(t, cfg, f2)
	err := v2.VerifyPassword(context.Background(), verifyDN, verifyPassword)
	if !errors.Is(err, authentication.ErrProviderUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if f2.boundUser != "" {
		t.Fatal("must not bind after a failed StartTLS (the password would be sent in clear text)")
	}
}

func TestVerifyPasswordContext(t *testing.T) {
	f := newFakeDirectory()
	v := newTestVerifier(t, adConfig(), f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := v.VerifyPassword(ctx, verifyDN, verifyPassword)
	if !errors.Is(err, authentication.ErrProviderUnavailable) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if f.dials != 0 {
		t.Fatal("cancelled context must not dial")
	}

	f2 := newFakeDirectory()
	f2.dialBlock = make(chan struct{})
	defer close(f2.dialBlock)
	v2 := newTestVerifier(t, adConfig(), f2)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	err = v2.VerifyPassword(ctx2, verifyDN, verifyPassword)
	if !errors.Is(err, authentication.ErrProviderUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

func TestVerifyPasswordSetsTimeoutFromDeadline(t *testing.T) {
	f := newFakeDirectory()
	v := newTestVerifier(t, adConfig(), f)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := v.VerifyPassword(ctx, verifyDN, verifyPassword); err != nil {
		t.Fatal(err)
	}
	if len(f.timeouts) == 0 || f.timeouts[0] > 5*time.Second {
		t.Fatalf("timeouts = %v", f.timeouts)
	}
}

func TestNewPasswordVerifierValidation(t *testing.T) {
	tests := []struct {
		name string
		mod  func(c *config.LDAPConfig)
	}{
		{"not configured", func(c *config.LDAPConfig) { c.URL = "" }},
		{"plain ldap", func(c *config.LDAPConfig) { c.URL = "ldap://dc.example.test" }},
		{"starttls with ldaps", func(c *config.LDAPConfig) { c.StartTLS = true }},
		{"bad scheme", func(c *config.LDAPConfig) { c.URL = "http://dc.example.test" }},
		{"missing CA file", func(c *config.LDAPConfig) { c.CAFile = "/nonexistent/ca.pem" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := adConfig()
			tt.mod(&cfg)
			if _, err := NewPasswordVerifier(cfg, nil); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	cfg := adConfig()
	cfg.URL = "ldap://dc.example.test"
	cfg.AllowPlaintext = true
	if _, err := NewPasswordVerifier(cfg, nil); err != nil {
		t.Fatalf("explicit plaintext opt-in: %v", err)
	}
}
