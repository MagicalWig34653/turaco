package kerberos

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jcmturner/gokrb5/v8/client"
	"github.com/jcmturner/gokrb5/v8/credentials"
	"github.com/jcmturner/gokrb5/v8/iana/etypeID"
	"github.com/jcmturner/gokrb5/v8/keytab"
	"github.com/jcmturner/gokrb5/v8/messages"
	"github.com/jcmturner/gokrb5/v8/spnego"
	"github.com/jcmturner/gokrb5/v8/types"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/authentication"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/config"
)

// These tests build real AP-REQ tokens in process with gokrb5's message types
// and the same keytab the validator loads, so the full verification path
// (decryption, skew, replay cache, realm and name rules) runs without a KDC.
// What they cannot cover is interoperability with real KDCs and browsers; that
// is exercised manually against an MIT KDC (see docs/product/current-status.md).

const (
	testRealm   = "EXAMPLE.TEST"
	testService = "HTTP/turaco.example.test"
	testKVNO    = 3
)

type testEnv struct {
	t         *testing.T
	kt        *keytab.Keytab
	file      string
	validator *Validator
	logs      *bytes.Buffer
}

func addKey(t *testing.T, kt *keytab.Keytab, principal, realm, password string) {
	t.Helper()
	if err := kt.AddEntry(principal, realm, password, time.Now(), testKVNO, etypeID.AES256_CTS_HMAC_SHA1_96); err != nil {
		t.Fatal(err)
	}
}

func writeKeytab(t *testing.T, kt *keytab.Keytab) string {
	t.Helper()
	b, err := kt.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "service.keytab")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func baseConfig(file string) config.KerberosConfig {
	return config.KerberosConfig{KeytabFile: file, ServicePrincipal: testService, Realm: testRealm, MaxClockSkew: 5 * time.Minute}
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	kt := keytab.New()
	addKey(t, kt, testService, testRealm, "service-key-password")
	// A second service in the same keytab must not make its tickets valid.
	addKey(t, kt, "HTTP/other.example.test", testRealm, "other-service-password")
	file := writeKeytab(t, kt)
	logs := &bytes.Buffer{}
	v, err := NewValidator(baseConfig(file), slog.New(slog.NewJSONHandler(logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return &testEnv{t: t, kt: kt, file: file, validator: v, logs: logs}
}

// ticketSpec describes one token; zero values mean "valid".
type ticketSpec struct {
	client      []string // client name components, default ["alice"]
	clientRealm string   // default testRealm
	service     string   // service principal the ticket is for, default testService
	serviceKey  string   // password the service key derives from; default: the keytab's
	ticketEnd   time.Time
	authTime    time.Time // authenticator time, default now
	raw         bool      // bare KRB5 token instead of SPNEGO
}

// token builds a SPNEGO NegTokenInit with an AP-REQ encrypted for the keytab.
func (e *testEnv) token(spec ticketSpec) []byte {
	e.t.Helper()
	if spec.client == nil {
		spec.client = []string{"alice"}
	}
	if spec.clientRealm == "" {
		spec.clientRealm = testRealm
	}
	if spec.service == "" {
		spec.service = testService
	}
	now := time.Now().UTC()
	if spec.ticketEnd.IsZero() {
		spec.ticketEnd = now.Add(10 * time.Hour)
	}
	if spec.authTime.IsZero() {
		spec.authTime = now
	}
	signingKeytab := e.kt
	if spec.serviceKey != "" {
		signingKeytab = keytab.New()
		addKey(e.t, signingKeytab, spec.service, testRealm, spec.serviceKey)
	}
	cname := types.PrincipalName{NameType: 1, NameString: spec.client}
	sname := types.NewPrincipalName(2, spec.service)
	tkt, sessionKey, err := messages.NewTicket(cname, spec.clientRealm, sname, testRealm, types.NewKrbFlags(),
		signingKeytab, etypeID.AES256_CTS_HMAC_SHA1_96, testKVNO, now.Add(-time.Minute), now.Add(-time.Minute), spec.ticketEnd, spec.ticketEnd)
	if err != nil {
		e.t.Fatal(err)
	}
	auth, err := types.NewAuthenticator(spec.clientRealm, cname)
	if err != nil {
		e.t.Fatal(err)
	}
	auth.CTime = spec.authTime
	var nonce [4]byte
	_, _ = rand.Read(nonce[:])
	auth.Cusec = int(nonce[0])<<16 | int(nonce[1])<<8 | int(nonce[2]) // unique per token for the replay cache
	apReq, err := messages.NewAPReq(tkt, sessionKey, auth)
	if err != nil {
		e.t.Fatal(err)
	}

	cl := &client.Client{Credentials: credentials.NewFromPrincipalName(cname, spec.clientRealm)}
	init, err := spnego.NewNegTokenInitKRB5(cl, tkt, sessionKey)
	if err != nil {
		e.t.Fatal(err)
	}
	mech, err := spnego.NewKRB5TokenAPREQ(cl, tkt, sessionKey, nil, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	mech.APReq = apReq
	if init.MechTokenBytes, err = mech.Marshal(); err != nil {
		e.t.Fatal(err)
	}
	if spec.raw {
		return init.MechTokenBytes
	}
	b, err := (&spnego.SPNEGOToken{Init: true, NegTokenInit: init}).Marshal()
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

func (e *testEnv) validate(token []byte) (Principal, error) {
	e.t.Helper()
	return e.validator.Validate(context.Background(), token)
}

func (e *testEnv) mustReject(token []byte, wantReason string) {
	e.t.Helper()
	p, err := e.validate(token)
	if !errors.Is(err, ErrInvalidTicket) || !errors.Is(err, authentication.ErrInvalidTicket) {
		e.t.Fatalf("err = %v, want ErrInvalidTicket", err)
	}
	if p != (Principal{}) {
		e.t.Fatalf("principal returned with an error: %+v", p)
	}
	if wantReason != "" && !strings.Contains(err.Error(), wantReason) {
		e.t.Fatalf("err = %q, want reason containing %q", err, wantReason)
	}
}

func TestValidateAcceptsSPNEGOToken(t *testing.T) {
	e := newEnv(t)
	p, err := e.validate(e.token(ticketSpec{}))
	if err != nil {
		t.Fatal(err)
	}
	if p != (Principal{Username: "alice", Realm: testRealm}) {
		t.Fatalf("principal = %+v", p)
	}
}

func TestValidateAcceptsRawKRB5Token(t *testing.T) {
	e := newEnv(t)
	p, err := e.validate(e.token(ticketSpec{raw: true}))
	if err != nil || p.Username != "alice" {
		t.Fatalf("principal = %+v, %v", p, err)
	}
}

func TestValidateRejectsReplay(t *testing.T) {
	e := newEnv(t)
	token := e.token(ticketSpec{})
	if _, err := e.validate(token); err != nil {
		t.Fatal(err)
	}
	e.mustReject(token, "replayed ticket")
}

func TestValidateRejectsOtherRealm(t *testing.T) {
	e := newEnv(t)
	e.mustReject(e.token(ticketSpec{clientRealm: "OTHER.TEST"}), "client realm not accepted")
	e.mustReject(e.token(ticketSpec{clientRealm: "example.test"}), "client realm not accepted")
}

func TestValidateRejectsStructuredClientNames(t *testing.T) {
	e := newEnv(t)
	for _, name := range [][]string{
		{"user", "admin"},   // instance principal
		{"host", "ws01"},    // service principal as client
		{"alice", "a", "b"}, // more components
		{""},                // empty
		{"EXAMPLE\\alice"},  // DOMAIN\user would hit the directory's alias rule
		{"alice@example.test"},
		{"alice/admin"},
		{"ali ce"},
		{"alice\n"},
		{strings.Repeat("a", maxUsernameBytes+1)},
		{"\xff\xfe"},
	} {
		e.mustReject(e.token(ticketSpec{client: name}), "client principal name not accepted")
	}
	if _, err := e.validate(e.token(ticketSpec{client: []string{"a.b-c_d$"}})); err != nil {
		t.Fatalf("ordinary account names must pass: %v", err)
	}
}

func TestValidateRejectsClockSkewAndExpiry(t *testing.T) {
	e := newEnv(t)
	e.mustReject(e.token(ticketSpec{authTime: time.Now().Add(-10 * time.Minute)}), "clock skew too large")
	e.mustReject(e.token(ticketSpec{authTime: time.Now().Add(10 * time.Minute)}), "clock skew too large")
	e.mustReject(e.token(ticketSpec{ticketEnd: time.Now().Add(-time.Hour)}), "ticket expired")
	// Within the skew the same clock difference is fine.
	if _, err := e.validate(e.token(ticketSpec{authTime: time.Now().Add(-2 * time.Minute)})); err != nil {
		t.Fatal(err)
	}
}

func TestValidateHonoursConfiguredSkew(t *testing.T) {
	e := newEnv(t)
	cfg := baseConfig(e.file)
	cfg.MaxClockSkew = 30 * time.Second
	v, err := NewValidator(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Validate(context.Background(), e.token(ticketSpec{authTime: time.Now().Add(-2 * time.Minute)})); !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("2m skew with a 30s limit: %v", err)
	}
}

func TestValidateRejectsTicketsForOtherServicesOrKeys(t *testing.T) {
	e := newEnv(t)
	// A ticket for another service whose key is in the keytab: the validator
	// only ever decrypts with the configured service principal's key.
	e.mustReject(e.token(ticketSpec{service: "HTTP/other.example.test", serviceKey: "other-service-password"}), "")
	// Right service name, key not shared with the KDC (wrong password).
	e.mustReject(e.token(ticketSpec{serviceKey: "not-the-service-password"}), "")
}

func TestValidateRejectsMalformedTokens(t *testing.T) {
	e := newEnv(t)
	valid := e.token(ticketSpec{})
	oversized := bytes.Repeat([]byte{0x60}, maxTokenBytes+1)
	for name, token := range map[string][]byte{
		"nil":            nil,
		"empty":          {},
		"one byte":       {0x60},
		"zeros":          make([]byte, 64),
		"text":           []byte("Negotiate abc"),
		"truncated":      valid[:len(valid)/2],
		"truncated tail": valid[:len(valid)-1],
		"oversized":      oversized,
		"neg token resp": {0xa1, 0x07, 0x30, 0x05, 0xa0, 0x03, 0x0a, 0x01, 0x02},
	} {
		t.Run(name, func(t *testing.T) { e.mustReject(token, "") })
	}
}

// Every single-byte corruption and every truncation of a valid token must end
// in the sentinel (or, for a harmless flip in an ignored field, success) and
// never in a panic or another error: gokrb5 parses attacker-controlled ASN.1
// and is known to panic on some inputs, which Validate recovers.
func TestValidateSurvivesCorruptedTokens(t *testing.T) {
	e := newEnv(t)
	valid := e.token(ticketSpec{})
	check := func(what string, b []byte) {
		t.Helper()
		if _, err := e.validate(b); err != nil && !errors.Is(err, ErrInvalidTicket) {
			t.Fatalf("%s: unexpected error %v", what, err)
		}
	}
	for pos := range valid {
		for _, mask := range []byte{0x01, 0x80, 0xff} {
			b := append([]byte(nil), valid...)
			b[pos] ^= mask
			check(fmt.Sprintf("flip %#x at %d", mask, pos), b)
		}
		check(fmt.Sprintf("truncate at %d", pos), valid[:pos])
	}
}

func TestValidateRespectsContext(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := e.validator.Validate(ctx, e.token(ticketSpec{}))
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestErrorsAndLogsContainNoTicketOrKeyMaterial(t *testing.T) {
	e := newEnv(t)
	secrets := []string{"mallory", "bob"}
	tokens := [][]byte{
		e.token(ticketSpec{client: []string{"mallory"}, clientRealm: "OTHER.TEST"}),
		e.token(ticketSpec{client: []string{"bob", "admin"}}),
		e.token(ticketSpec{client: []string{"mallory"}, authTime: time.Now().Add(-time.Hour)}),
	}
	for _, token := range tokens {
		_, err := e.validate(token)
		if err == nil {
			t.Fatal("expected a rejection")
		}
		for _, s := range append(secrets, base64.StdEncoding.EncodeToString(token[:24])) {
			if strings.Contains(err.Error(), s) || strings.Contains(e.logs.String(), s) {
				t.Fatalf("%q leaked into an error or the log: %v / %s", s, err, e.logs)
			}
		}
	}
	for _, entry := range e.kt.Entries {
		for _, enc := range []string{hex.EncodeToString(entry.Key.KeyValue), base64.StdEncoding.EncodeToString(entry.Key.KeyValue)} {
			if strings.Contains(e.logs.String(), enc) {
				t.Fatal("key material was logged")
			}
		}
	}
	if !strings.Contains(e.logs.String(), "kerberos ticket rejected") {
		t.Fatalf("rejections should be logged with a reason: %s", e.logs)
	}
}

func TestNewValidatorLoadsKeytabOnce(t *testing.T) {
	e := newEnv(t)
	token := e.token(ticketSpec{})
	if err := os.Remove(e.file); err != nil {
		t.Fatal(err)
	}
	if _, err := e.validate(token); err != nil {
		t.Fatalf("the keytab must be read once at startup: %v", err)
	}
}

func TestNewValidatorConfigErrors(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	garbage := filepath.Join(dir, "garbage.keytab")
	if err := os.WriteFile(garbage, []byte("this is not a keytab, key=hunter2"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty.keytab")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	full, _ := e.kt.Marshal()
	truncated := filepath.Join(dir, "truncated.keytab")
	if err := os.WriteFile(truncated, full[:len(full)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	otherRealm := keytab.New()
	addKey(t, otherRealm, testService, "OTHER.TEST", "pw")
	otherSPN := keytab.New()
	addKey(t, otherSPN, "HTTP/another.example.test", testRealm, "pw")
	instanceOnly := keytab.New()
	addKey(t, instanceOnly, "HTTP", testRealm, "pw")

	mod := func(f func(*config.KerberosConfig)) config.KerberosConfig {
		c := baseConfig(e.file)
		f(&c)
		return c
	}
	for name, tc := range map[string]struct {
		cfg  config.KerberosConfig
		want string
	}{
		"disabled":              {config.KerberosConfig{}, "not configured"},
		"missing file":          {mod(func(c *config.KerberosConfig) { c.KeytabFile = filepath.Join(dir, "missing") }), "does not exist"},
		"directory":             {mod(func(c *config.KerberosConfig) { c.KeytabFile = dir }), "cannot be read"},
		"garbage":               {mod(func(c *config.KerberosConfig) { c.KeytabFile = garbage }), "not a valid keytab"},
		"empty":                 {mod(func(c *config.KerberosConfig) { c.KeytabFile = empty }), "not a valid keytab"},
		"truncated":             {mod(func(c *config.KerberosConfig) { c.KeytabFile = truncated }), ""},
		"key for other realm":   {mod(func(c *config.KerberosConfig) { c.KeytabFile = writeKeytab(t, otherRealm) }), "no key for the service principal"},
		"key for other service": {mod(func(c *config.KerberosConfig) { c.KeytabFile = writeKeytab(t, otherSPN) }), "no key for the service principal"},
		"key for one component": {mod(func(c *config.KerberosConfig) { c.KeytabFile = writeKeytab(t, instanceOnly) }), "no key for the service principal"},
		"realm missing":         {mod(func(c *config.KerberosConfig) { c.Realm = "" }), "realm"},
		"realm lower case":      {mod(func(c *config.KerberosConfig) { c.Realm = "example.test" }), "realm"},
		"principal missing":     {mod(func(c *config.KerberosConfig) { c.ServicePrincipal = "" }), "service principal"},
		"principal with realm":  {mod(func(c *config.KerberosConfig) { c.ServicePrincipal = testService + "@" + testRealm }), "service principal"},
		"principal one part":    {mod(func(c *config.KerberosConfig) { c.ServicePrincipal = "HTTP" }), "service principal"},
		"negative skew":         {mod(func(c *config.KerberosConfig) { c.MaxClockSkew = -time.Second }), "clock skew"},
		"huge skew":             {mod(func(c *config.KerberosConfig) { c.MaxClockSkew = time.Hour }), "clock skew"},
	} {
		t.Run(name, func(t *testing.T) {
			v, err := NewValidator(tc.cfg, nil)
			if err == nil || v != nil {
				t.Fatalf("NewValidator = %v, %v; want an error", v, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %q, want %q", err, tc.want)
			}
			for _, leak := range []string{"hunter2", "this is not a keytab"} {
				if strings.Contains(err.Error(), leak) {
					t.Fatalf("keytab content leaked into the error: %v", err)
				}
			}
		})
	}
}

func TestNewValidatorDefaultsSkew(t *testing.T) {
	e := newEnv(t)
	cfg := baseConfig(e.file)
	cfg.MaxClockSkew = 0
	v, err := NewValidator(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := v.settings.MaxClockSkew(); got != 5*time.Minute {
		t.Fatalf("skew = %v", got)
	}
}

func TestNewValidatorLogsNoKeyMaterial(t *testing.T) {
	e := newEnv(t)
	for _, entry := range e.kt.Entries {
		if strings.Contains(e.logs.String(), hex.EncodeToString(entry.Key.KeyValue)) {
			t.Fatal("key material was logged")
		}
	}
	if !strings.Contains(e.logs.String(), "kerberos login configured") {
		t.Fatalf("startup should be logged: %s", e.logs)
	}
}

func TestValidUsername(t *testing.T) {
	for s, want := range map[string]bool{
		"alice": true, "a.b": true, "svc-x$": true, "Ünal": true,
		"": false, "a b": false, "a@b": false, `D\a`: false, "a/b": false, "a\x00": false, "a b": false,
	} {
		if got := validUsername(s); got != want {
			t.Errorf("validUsername(%q) = %v, want %v", s, got, want)
		}
	}
}
