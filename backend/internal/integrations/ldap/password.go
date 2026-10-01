package ldap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sync"

	goldap "github.com/go-ldap/ldap/v3"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authentication"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
)

// PasswordVerifier checks a user's directory password by binding as the
// account's distinguished name over LDAPS or StartTLS. It needs no service
// account and uses the same TLS policy and CA as the synchronization Source.
// It implements authentication.PasswordVerifier and is safe for concurrent
// use; every call uses its own connection.
type PasswordVerifier struct {
	// conn reuses the dialing and timeout helpers of Source; it holds no
	// bind credentials.
	conn *Source
}

var _ authentication.PasswordVerifier = (*PasswordVerifier)(nil)

// NewPasswordVerifier validates the connection settings (config.LoadLDAPConnection
// is enough) and prepares the TLS configuration. It performs no network access.
func NewPasswordVerifier(cfg config.LDAPConfig, logger *slog.Logger) (*PasswordVerifier, error) {
	if !cfg.Enabled() {
		return nil, errors.New("ldap: directory password login is not configured")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, errors.New("ldap: directory URL is invalid")
	}
	switch u.Scheme {
	case "ldaps":
		if cfg.StartTLS {
			return nil, errors.New("ldap: StartTLS must not be combined with an ldaps URL")
		}
	case "ldap":
		if !cfg.StartTLS {
			// User passwords must not cross the network in clear text
			// unless the development opt-in is explicit.
			if !cfg.AllowPlaintext {
				return nil, errors.New("ldap: plain ldap:// without StartTLS is not allowed (requires explicit plaintext opt-in)")
			}
			logger.Warn("ldap: password verification is not encrypted", "providerKey", cfg.ProviderKey)
		}
	default:
		return nil, errors.New("ldap: directory URL must use ldaps or ldap")
	}
	tlsConfig, err := buildTLSConfig(cfg.URL, cfg.CAFile)
	if err != nil {
		return nil, err
	}
	return &PasswordVerifier{conn: &Source{cfg: cfg, tlsConfig: tlsConfig, dial: dialLDAP, logger: logger}}, nil
}

// VerifyPassword binds as dn with password. It returns nil on success,
// authentication.ErrInvalidCredentials when the directory rejects the
// credentials (result code 49) and authentication.ErrProviderUnavailable for
// connection, TLS and any other directory failure. An empty password is
// refused before any network access: an empty simple bind is an
// unauthenticated bind that servers accept. The password never appears in
// returned errors or logs.
func (v *PasswordVerifier) VerifyPassword(ctx context.Context, dn, password string) error {
	if dn == "" || password == "" {
		return authentication.ErrInvalidCredentials
	}
	if err := ctx.Err(); err != nil {
		return unavailable(opError(ctx, "connect", err))
	}
	conn, err := v.conn.connect(ctx)
	if err != nil {
		return unavailable(err)
	}
	var closeOnce sync.Once
	closeConn := func() { closeOnce.Do(func() { _ = conn.Close() }) }
	defer closeConn()
	// Closing the connection is the only way to interrupt a blocked request.
	stop := context.AfterFunc(ctx, closeConn)
	defer stop()

	if _, err := v.conn.setTimeout(ctx, conn); err != nil {
		return unavailable(err)
	}
	if v.conn.cfg.StartTLS {
		if err := conn.StartTLS(v.conn.tlsConfig); err != nil {
			return unavailable(opError(ctx, "starttls", err))
		}
	}
	if _, err := v.conn.setTimeout(ctx, conn); err != nil {
		return unavailable(err)
	}
	if err := conn.Bind(dn, password); err != nil {
		if ctx.Err() == nil && goldap.IsErrorWithCode(err, goldap.LDAPResultInvalidCredentials) {
			return authentication.ErrInvalidCredentials
		}
		return unavailable(opError(ctx, "bind", err))
	}
	return nil
}

// unavailable wraps a sanitized operation error (opError never carries server
// diagnostics, DNs or passwords).
func unavailable(err error) error {
	return fmt.Errorf("%w: %w", authentication.ErrProviderUnavailable, err)
}
