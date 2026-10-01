package kerberos

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jcmturner/gokrb5/v8/gssapi"
	"github.com/jcmturner/gokrb5/v8/iana/errorcode"
	"github.com/jcmturner/gokrb5/v8/keytab"
	"github.com/jcmturner/gokrb5/v8/messages"
	"github.com/jcmturner/gokrb5/v8/service"
	"github.com/jcmturner/gokrb5/v8/spnego"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authentication"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
)

const (
	// maxTokenBytes bounds the token accepted for parsing; real tickets,
	// including large Windows PACs, stay far below it.
	maxTokenBytes = 64 << 10
	// maxUsernameBytes matches the login identifier limit.
	maxUsernameBytes = 256
	defaultSkew      = 5 * time.Minute
)

// Principal is the authenticated client: user name without realm and the
// realm. It is the type the authentication package consumes.
type Principal = authentication.KerberosPrincipal

// ErrInvalidTicket is returned (wrapped, with a fixed reason) for every
// problem with a ticket.
var ErrInvalidTicket = authentication.ErrInvalidTicket

// Validator validates SPNEGO tokens against one keytab, service principal and
// realm. It is immutable after NewValidator and safe for concurrent use.
type Validator struct {
	settings *service.Settings
	realm    string
	logger   *slog.Logger
	// verifyMu serializes ticket verification: gokrb5's replay cache checks
	// and records an authenticator in two unlocked steps, so concurrent
	// presentations of one captured token could otherwise both pass.
	// Verification is fast symmetric crypto, so serializing is cheap.
	verifyMu sync.Mutex
}

// acceptedKeyTypes are the AES encryption types accepted from the keytab
// (aes128/256-cts-hmac-sha1-96 and -sha256/384). RC4-HMAC, DES and DES3 keys
// are ignored even if present.
var acceptedKeyTypes = map[int32]bool{17: true, 18: true, 19: true, 20: true}

var _ authentication.KerberosValidator = (*Validator)(nil)

// NewValidator loads the keytab once and checks that it holds a key for the
// service principal in the configured realm. It performs no network access.
func NewValidator(cfg config.KerberosConfig, logger *slog.Logger) (*Validator, error) {
	if !cfg.Enabled() {
		return nil, errors.New("kerberos: login is not configured")
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if cfg.Realm == "" || cfg.Realm != strings.ToUpper(cfg.Realm) {
		return nil, errors.New("kerberos: realm must be set and upper case")
	}
	components := strings.Split(cfg.ServicePrincipal, "/")
	if len(components) != 2 || components[0] != "HTTP" || components[1] == "" || strings.Contains(cfg.ServicePrincipal, "@") {
		return nil, errors.New("kerberos: service principal must be HTTP/<host name> without a realm")
	}
	skew := cfg.MaxClockSkew
	switch {
	case skew == 0:
		skew = defaultSkew
	case skew < 0 || skew > config.MaxKerberosClockSkew:
		return nil, fmt.Errorf("kerberos: clock skew must be positive and at most %s", config.MaxKerberosClockSkew)
	}

	all, err := loadKeytab(cfg.KeytabFile)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(cfg.KeytabFile); err == nil && info.Mode().Perm()&0o077 != 0 {
		// Docker/Swarm secrets are often mounted world-readable inside the
		// container; warn rather than refuse.
		logger.Warn("kerberos keytab file is readable by group or others; restrict it to the API process", "mode", info.Mode().Perm().String())
	}
	// Keep only AES keys of the configured service principal in the
	// configured realm: gokrb5 selects the key by the ticket's unencrypted
	// realm, so a key of another realm in the same keytab would let that
	// realm's KDC mint tickets for our users.
	kt := keytab.New()
	for _, e := range all.Entries {
		if e.Principal.Realm == cfg.Realm && equalStrings(e.Principal.Components, components) && acceptedKeyTypes[e.Key.KeyType] {
			kt.Entries = append(kt.Entries, e)
		}
	}
	keys := len(kt.Entries)
	if keys == 0 {
		return nil, errors.New("kerberos: keytab has no AES key for the service principal in the configured realm")
	}
	logger.Info("kerberos login configured", "servicePrincipal", cfg.ServicePrincipal, "realm", cfg.Realm, "keys", keys, "maxClockSkew", skew.String())

	return &Validator{
		settings: service.NewSettings(kt,
			// The ticket is decrypted with the configured service
			// principal's key, never with whatever principal the ticket
			// names.
			service.KeytabPrincipal(cfg.ServicePrincipal),
			service.MaxClockSkew(skew),
			// Groups come from the synchronized directory; the PAC is not
			// needed and not parsed.
			service.DecodePAC(false),
		),
		realm:  cfg.Realm,
		logger: logger,
	}, nil
}

// loadKeytab reads the keytab file. Errors carry the failed step only: never
// file contents, and no gokrb5 diagnostics.
func loadKeytab(path string) (kt *keytab.Keytab, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist):
			return nil, errors.New("kerberos: keytab file does not exist")
		case errors.Is(err, os.ErrPermission):
			return nil, errors.New("kerberos: keytab file is not readable")
		}
		return nil, errors.New("kerberos: keytab file cannot be read")
	}
	defer func() {
		if recover() != nil {
			kt, err = nil, errors.New("kerberos: keytab file is not a valid keytab")
		}
	}()
	kt = keytab.New()
	if err := kt.Unmarshal(b); err != nil {
		return nil, errors.New("kerberos: keytab file is not a valid keytab")
	}
	return kt, nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Validate verifies an RFC 4178 SPNEGO NegTokenInit (or a raw RFC 4121 KRB5
// token) carrying a KRB5 AP-REQ. It returns the client's user name and realm,
// or an error wrapping ErrInvalidTicket for any ticket problem.
func (v *Validator) Validate(ctx context.Context, token []byte) (p Principal, err error) {
	if err := ctx.Err(); err != nil {
		return Principal{}, err
	}
	defer func() {
		// gokrb5 parses attacker-controlled ASN.1; a panic must not take the
		// API down. It is a rejected ticket, logged without any detail.
		if recover() != nil {
			v.logger.Warn("kerberos ticket rejected", "reason", "parser failure")
			p, err = Principal{}, invalid("malformed token")
		}
	}()

	p, err = v.validate(token)
	var rejected *rejection
	if errors.As(err, &rejected) {
		v.logger.Warn("kerberos ticket rejected", "reason", rejected.reason)
	}
	return p, err
}

func (v *Validator) validate(token []byte) (Principal, error) {
	if len(token) == 0 || len(token) > maxTokenBytes {
		return Principal{}, invalid("token size")
	}
	mech, err := parseMechToken(token)
	if err != nil {
		return Principal{}, err
	}
	if !mech.IsAPReq() {
		return Principal{}, invalid("not an AP-REQ")
	}
	// The outer ticket realm is unauthenticated but selects the decryption
	// key; only our realm is accepted.
	if mech.APReq.Ticket.Realm != v.realm {
		return Principal{}, invalid("ticket realm not accepted")
	}
	ok, err := v.verify(&mech.APReq)
	if err != nil || !ok {
		return Principal{}, invalid(describe(err))
	}
	// Identity comes from the KDC-encrypted ticket part, never from the
	// authenticator: the client encrypts the authenticator itself and gokrb5
	// compares only the names, not the realms. A user of a trusted realm
	// could otherwise claim our realm.
	enc := mech.APReq.Ticket.DecryptedEncPart
	if enc.CRealm != v.realm || mech.APReq.Authenticator.CRealm != v.realm {
		return Principal{}, invalid("client realm not accepted")
	}
	// A single component: user/admin or host/name principals are service or
	// instance identities, never a person's login.
	name := enc.CName.NameString
	if len(name) != 1 || !validUsername(name[0]) {
		return Principal{}, invalid("client principal name not accepted")
	}
	return Principal{Username: name[0], Realm: enc.CRealm}, nil
}

// verify runs gokrb5's AP-REQ verification under verifyMu. The deferred
// unlock matters: gokrb5 can panic on malformed input, and Validate recovers
// the panic further up.
func (v *Validator) verify(apReq *messages.APReq) (bool, error) {
	v.verifyMu.Lock()
	defer v.verifyMu.Unlock()
	ok, _, err := service.VerifyAPREQ(apReq, v.settings)
	return ok, err
}

// parseMechToken extracts the KRB5 mechanism token: from a SPNEGO
// NegTokenInit, or, like browsers of some stacks, a bare KRB5 token.
func parseMechToken(token []byte) (*spnego.KRB5Token, error) {
	var mech spnego.KRB5Token
	var st spnego.SPNEGOToken
	if err := st.Unmarshal(token); err != nil {
		if mech.Unmarshal(token) != nil {
			return nil, invalid("malformed token")
		}
		return &mech, nil
	}
	if !st.Init || st.Resp {
		return nil, invalid("not a NegTokenInit")
	}
	krb5Offered := false
	for _, oid := range st.NegTokenInit.MechTypes {
		if oid.Equal(gssapi.OIDKRB5.OID()) || oid.Equal(gssapi.OIDMSLegacyKRB5.OID()) {
			krb5Offered = true
		}
	}
	if !krb5Offered || len(st.NegTokenInit.MechTokenBytes) == 0 {
		return nil, invalid("no kerberos mechanism token")
	}
	if err := mech.Unmarshal(st.NegTokenInit.MechTokenBytes); err != nil {
		return nil, invalid("malformed mechanism token")
	}
	return &mech, nil
}

// validUsername accepts a plain login name: valid UTF-8, bounded, no control
// or space characters and none of the separators that the directory lookup
// interprets (`DOMAIN\user`, `user@host`) or that make a Kerberos name
// structured (`/`).
func validUsername(s string) bool {
	if s == "" || len(s) > maxUsernameBytes || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r == '@' || r == '\\' || r == '/' || unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// rejection is the error of a rejected ticket: it wraps ErrInvalidTicket and
// carries a fixed reason for operators, never ticket contents.
type rejection struct{ reason string }

func (r *rejection) Error() string { return "kerberos: invalid ticket: " + r.reason }
func (r *rejection) Unwrap() error { return ErrInvalidTicket }

func invalid(reason string) error { return &rejection{reason: reason} }

// describe maps a gokrb5 verification error to a fixed reason. Only the
// Kerberos error code is used; gokrb5's message text is dropped.
func describe(err error) string {
	var krbErr messages.KRBError
	if errors.As(err, &krbErr) {
		switch krbErr.ErrorCode {
		case errorcode.KRB_AP_ERR_SKEW:
			return "clock skew too large"
		case errorcode.KRB_AP_ERR_REPEAT:
			return "replayed ticket"
		case errorcode.KRB_AP_ERR_NOKEY:
			return "no matching service key (wrong service principal, key version or encryption type)"
		case errorcode.KRB_AP_ERR_TKT_EXPIRED:
			return "ticket expired"
		case errorcode.KRB_AP_ERR_TKT_NYV:
			return "ticket not yet valid"
		case errorcode.KRB_AP_ERR_BADADDR:
			return "client address not allowed by the ticket"
		case errorcode.KRB_AP_ERR_BADMATCH:
			return "ticket and authenticator do not match"
		}
	}
	return "ticket could not be verified"
}
