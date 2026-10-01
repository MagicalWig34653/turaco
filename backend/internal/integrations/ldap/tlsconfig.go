package ldap

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"os"
)

// buildTLSConfig returns the client TLS configuration used for LDAPS and
// StartTLS. Verification is always enabled: the system pool is trusted, plus
// the certificates in caFile when set. ServerName is the host of the
// directory URL, so StartTLS (where the TLS library has no address to derive
// it from) verifies the same name as LDAPS.
func buildTLSConfig(rawURL, caFile string) (*tls.Config, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return nil, errors.New("ldap: directory URL must contain a host")
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("ldap: load system certificate pool: %w", err)
	}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("ldap: read CA file: %w", err)
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("ldap: CA file contains no valid PEM certificates")
		}
	}
	return &tls.Config{
		RootCAs:    pool,
		ServerName: u.Hostname(),
		MinVersion: tls.VersionTLS12,
	}, nil
}
