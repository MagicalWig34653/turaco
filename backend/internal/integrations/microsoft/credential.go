package microsoft

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Credential adds the client authentication of a confidential client to a token request. Credentials come from
// deployment files; the database never holds them (ADR-0014).
type Credential interface {
	// Apply adds client_secret, or client_assertion and client_assertion_type, to form.
	Apply(form url.Values, clientID, tokenEndpoint string) error
	// Kind is "secret" or "certificate" (for health output).
	Kind() string
	// ExpiresAt reports the expiry when it is known: the certificate's NotAfter, or the operator-set secret expiry.
	ExpiresAt() (time.Time, bool)
}

// secretFileCredential reads the secret file again whenever its modification time changes (rotation without restart).
type secretFileCredential struct {
	path    string
	expires *time.Time
	mu      sync.Mutex
	mtime   time.Time
	secret  string
}

// NewSecretCredential validates that the file is readable and returns a credential that re-reads it on change.
func NewSecretCredential(path string, expiresAt *time.Time) (Credential, error) {
	c := &secretFileCredential{path: path, expires: expiresAt}
	if _, err := c.current(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *secretFileCredential) current() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fi, err := os.Stat(c.path)
	if err != nil {
		return "", fmt.Errorf("client secret file: %w", errors.Unwrap(err))
	}
	if c.secret != "" && fi.ModTime().Equal(c.mtime) {
		return c.secret, nil
	}
	data, err := os.ReadFile(c.path)
	if err != nil {
		return "", fmt.Errorf("client secret file: %w", errors.Unwrap(err))
	}
	s := strings.TrimRight(string(data), "\r\n")
	if s == "" || len(data) > 64<<10 {
		return "", errors.New("client secret file is empty or too large")
	}
	c.secret, c.mtime = s, fi.ModTime()
	return s, nil
}

func (c *secretFileCredential) Apply(form url.Values, _, _ string) error {
	s, err := c.current()
	if err != nil {
		return err
	}
	form.Set("client_secret", s)
	return nil
}

func (c *secretFileCredential) Kind() string { return "secret" }

func (c *secretFileCredential) ExpiresAt() (time.Time, bool) {
	if c.expires == nil {
		return time.Time{}, false
	}
	return *c.expires, true
}

// certificateCredential authenticates with a signed client assertion (RFC 7523) instead of a secret.
type certificateCredential struct {
	key      *rsa.PrivateKey
	thumb    string // base64url SHA-256 of the DER certificate (x5t#S256)
	notAfter time.Time
	now      func() time.Time
}

// NewCertificateCredential loads a PEM certificate and its RSA private key.
func NewCertificateCredential(certFile, keyFile string) (Credential, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return nil, fmt.Errorf("client certificate file: %w", errors.Unwrap(err))
	}
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("client certificate file holds no PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, errors.New("client certificate cannot be parsed")
	}
	keyPEM, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("client private key file: %w", errors.Unwrap(err))
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, errors.New("client private key file holds no PEM key")
	}
	var key *rsa.PrivateKey
	if k, perr := x509.ParsePKCS1PrivateKey(kb.Bytes); perr == nil {
		key = k
	} else if k8, perr := x509.ParsePKCS8PrivateKey(kb.Bytes); perr == nil {
		rk, ok := k8.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("client private key must be RSA")
		}
		key = rk
	} else {
		return nil, errors.New("client private key cannot be parsed")
	}
	if pub, ok := cert.PublicKey.(*rsa.PublicKey); !ok || pub.N.Cmp(key.N) != 0 || pub.E != key.E {
		return nil, errors.New("client certificate and private key do not match")
	}
	sum := sha256.Sum256(cert.Raw)
	return &certificateCredential{key: key, thumb: base64.RawURLEncoding.EncodeToString(sum[:]), notAfter: cert.NotAfter, now: time.Now}, nil
}

func (c *certificateCredential) Kind() string { return "certificate" }

func (c *certificateCredential) ExpiresAt() (time.Time, bool) { return c.notAfter, true }

func (c *certificateCredential) Apply(form url.Values, clientID, tokenEndpoint string) error {
	now := c.now().UTC()
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return err
	}
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "x5t#S256": c.thumb})
	claims, _ := json.Marshal(map[string]any{
		"aud": tokenEndpoint, "iss": clientID, "sub": clientID, "jti": base64.RawURLEncoding.EncodeToString(jti),
		"nbf": now.Unix(), "iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
	})
	signing := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.key, crypto.SHA256, digest[:])
	if err != nil {
		return err
	}
	form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
	form.Set("client_assertion", signing+"."+base64.RawURLEncoding.EncodeToString(sig))
	return nil
}

// ExpiryStatus classifies days to expiry for health output: attention at 30 days, failing after expiry.
func ExpiryStatus(expiresAt time.Time, now time.Time) (days int, state string) {
	d := expiresAt.Sub(now)
	days = int(d.Hours() / 24)
	switch {
	case d <= 0:
		return days, "expired"
	case d < 30*24*time.Hour:
		return days, "attention"
	default:
		return days, "ok"
	}
}
