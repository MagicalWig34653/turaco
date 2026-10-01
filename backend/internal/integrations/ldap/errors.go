package ldap

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"

	goldap "github.com/go-ldap/ldap/v3"
)

// opError builds the error returned for a failed LDAP operation. It carries
// the operation and, when go-ldap reports one, the numeric result code and its
// standard name. It deliberately drops the wrapped error text: go-ldap
// includes the server's diagnostic message there, and servers echo DNs and
// attribute values in them. A context error is preserved (errors.Is works)
// because it carries no directory data.
func opError(ctx context.Context, op string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("ldap: %s: %w", op, ctxErr)
	}
	msg := "unexpected error"
	var ldapErr *goldap.Error
	if errors.As(err, &ldapErr) {
		name := goldap.LDAPResultCodeMap[ldapErr.ResultCode]
		if name == "" {
			name = "unknown"
		}
		msg = fmt.Sprintf("result code %d (%s)", ldapErr.ResultCode, name)
	}
	if reason := transportReason(err); reason != "" {
		msg += ": " + reason
	}
	return fmt.Errorf("ldap: %s: %s", op, msg)
}

// transportReason maps well-known TLS and network failures to fixed strings
// that help operators fix certificate or connectivity problems without
// repeating certificate subjects or addresses.
func transportReason(err error) string {
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var netErr net.Error
	switch {
	case errors.As(err, &unknownAuthority):
		return "certificate signed by unknown authority"
	case errors.As(err, &hostname):
		return "certificate does not match the server name"
	case errors.As(err, &invalid):
		return "certificate is not valid"
	case errors.As(err, &netErr) && netErr.Timeout():
		return "timeout"
	}
	return ""
}
