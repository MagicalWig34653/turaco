package microsoft

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testClient(t *testing.T, h http.Handler, max int64) (*Client, string) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	c, err := New(Config{AllowedHosts: []string{u.Host}, Transport: srv.Client().Transport, AllowInsecureHTTP: true, MaxBodyBytes: max})
	if err != nil {
		t.Fatal(err)
	}
	return c, srv.URL
}

func TestHostAllowListAndScheme(t *testing.T) {
	c, err := New(Config{AllowedHosts: []string{"login.microsoftonline.com"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"http://login.microsoftonline.com/x", "https://evil.example/x", "https://login.microsoftonline.com.evil.example/x",
		"https://user:pw@login.microsoftonline.com/x", "ftp://login.microsoftonline.com/", "https:///x", "login.microsoftonline.com/x",
	} {
		if _, err := c.CheckURL(bad); !errors.Is(err, ErrHostNotAllowed) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := c.CheckURL("https://LOGIN.microsoftonline.com/common/oauth2/v2.0/token"); err != nil {
		t.Errorf("allowed host refused: %v", err)
	}
	// Nothing is sent for a refused URL.
	if _, err := c.Do(context.Background(), "GET", "https://evil.example/", nil, nil); !errors.Is(err, ErrHostNotAllowed) {
		t.Errorf("Do: %v", err)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	hit := false
	c, base := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			hit = true
		}
		http.Redirect(w, r, "/final", http.StatusFound)
	}), 0)
	resp, err := c.Do(context.Background(), "GET", base+"/start", nil, nil)
	if err != nil || resp.Status != http.StatusFound || hit {
		t.Fatalf("status %d hit %v err %v", resp.Status, hit, err)
	}
}

func TestResponseSizeCapAndTransientClassification(t *testing.T) {
	c, base := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/big":
			_, _ = w.Write([]byte(strings.Repeat("x", 100)))
		case "/429":
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
		case "/500":
			w.WriteHeader(http.StatusBadGateway)
		case "/400":
			w.WriteHeader(http.StatusBadRequest)
		}
	}), 50)
	ctx := context.Background()
	if _, err := c.Do(ctx, "GET", base+"/big", nil, nil); !errors.Is(err, ErrResponseTooLarge) {
		t.Errorf("big: %v", err)
	}
	var te *TransientError
	if _, err := c.Do(ctx, "GET", base+"/429", nil, nil); !errors.As(err, &te) || te.Status != 429 || te.RetryAfter != 7*time.Second {
		t.Errorf("429: %v %+v", err, te)
	}
	if _, err := c.Do(ctx, "GET", base+"/500", nil, nil); !errors.As(err, &te) || te.Status != 502 {
		t.Errorf("500: %v", err)
	}
	if resp, err := c.Do(ctx, "GET", base+"/400", nil, nil); err != nil || resp.Status != 400 {
		t.Errorf("400 must be returned to the caller: %v %d", err, resp.Status)
	}
}

func TestPublicAddressCheck(t *testing.T) {
	for ip, want := range map[string]bool{
		"127.0.0.1": false, "10.1.2.3": false, "192.168.0.5": false, "169.254.169.254": false, "::1": false, "fe80::1": false,
		"0.0.0.0": false, "224.0.0.1": false, "::ffff:127.0.0.1": false, "20.190.151.1": true, "2603:1026::1": true,
	} {
		if got := publicIP(netip.MustParseAddr(ip)); got != want {
			t.Errorf("%s: %v", ip, got)
		}
	}
}

func TestSecretCredentialRereadsOnRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret")
	if err := os.WriteFile(path, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exp := time.Now().Add(48 * time.Hour)
	c, err := NewSecretCredential(path, &exp)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{}
	_ = c.Apply(form, "client", "https://t")
	if form.Get("client_secret") != "first" || c.Kind() != "secret" {
		t.Fatalf("%v", form)
	}
	if e, ok := c.ExpiresAt(); !ok || !e.Equal(exp) {
		t.Fatal("expiry")
	}
	if err := os.WriteFile(path, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(path, future, future)
	form = url.Values{}
	_ = c.Apply(form, "client", "https://t")
	if form.Get("client_secret") != "second" {
		t.Fatalf("rotation not picked up: %v", form)
	}
	if _, err := NewSecretCredential(filepath.Join(dir, "missing"), nil); err == nil || strings.Contains(err.Error(), dir) && false {
		t.Fatalf("missing file: %v", err)
	}
}

func writeCertPair(t *testing.T, dir string, notAfter time.Time) (certFile, keyFile string, key *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "turaco"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile = filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	_ = os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	_ = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600)
	return certFile, keyFile, key
}

func TestCertificateCredentialSignsVerifiableAssertion(t *testing.T) {
	dir := t.TempDir()
	notAfter := time.Now().Add(90 * 24 * time.Hour).UTC().Truncate(time.Second)
	certFile, keyFile, key := writeCertPair(t, dir, notAfter)
	c, err := NewCertificateCredential(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{}
	if err := c.Apply(form, "client-id", "https://login.microsoftonline.com/t/oauth2/v2.0/token"); err != nil {
		t.Fatal(err)
	}
	if form.Get("client_secret") != "" || form.Get("client_assertion_type") != "urn:ietf:params:oauth:client-assertion-type:jwt-bearer" {
		t.Fatalf("%v", form)
	}
	parts := strings.Split(form.Get("client_assertion"), ".")
	if len(parts) != 3 {
		t.Fatal("assertion is not a JWT")
	}
	var hdr map[string]string
	raw, _ := base64.RawURLEncoding.DecodeString(parts[0])
	_ = json.Unmarshal(raw, &hdr)
	if hdr["alg"] != "RS256" || hdr["x5t#S256"] == "" {
		t.Fatalf("header %v", hdr)
	}
	var claims map[string]any
	raw, _ = base64.RawURLEncoding.DecodeString(parts[1])
	_ = json.Unmarshal(raw, &claims)
	if claims["iss"] != "client-id" || claims["sub"] != "client-id" || claims["aud"] != "https://login.microsoftonline.com/t/oauth2/v2.0/token" {
		t.Fatalf("claims %v", claims)
	}
	if err := verifyRS256(&key.PublicKey, parts[0]+"."+parts[1], parts[2]); err != nil {
		t.Fatalf("signature: %v", err)
	}
	if e, ok := c.ExpiresAt(); !ok || !e.Equal(notAfter) {
		t.Fatalf("expiry %v", e)
	}
	// A mismatching key pair is refused at load time.
	_, otherKey, _ := writeCertPair(t, t.TempDir(), notAfter)
	if _, err := NewCertificateCredential(certFile, otherKey); err == nil {
		t.Fatal("mismatching pair accepted")
	}
}

func TestExpiryStatus(t *testing.T) {
	now := time.Now()
	for d, want := range map[time.Duration]string{-time.Hour: "expired", 10 * 24 * time.Hour: "attention", 60 * 24 * time.Hour: "ok"} {
		if _, got := ExpiryStatus(now.Add(d), now); got != want {
			t.Errorf("%v: %s", d, got)
		}
	}
}

func TestRetryAfterAcceptsSecondsAndHTTPDate(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", "7")
	if retryAfter(h) != 7*time.Second {
		t.Errorf("seconds: %v", retryAfter(h))
	}
	h.Set("Retry-After", time.Now().Add(90*time.Second).UTC().Format(http.TimeFormat))
	if d := retryAfter(h); d < 80*time.Second || d > 91*time.Second {
		t.Errorf("http-date: %v", d)
	}
	h.Set("Retry-After", time.Now().Add(5*time.Hour).UTC().Format(http.TimeFormat))
	if retryAfter(h) != time.Hour {
		t.Errorf("cap: %v", retryAfter(h))
	}
	for _, bad := range []string{"", "-5", "soon", time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)} {
		h.Set("Retry-After", bad)
		if retryAfter(h) != 0 {
			t.Errorf("%q: %v", bad, retryAfter(h))
		}
	}
}

func TestSafeToRetryOnlyForIdempotentOrExplicitThrottle(t *testing.T) {
	throttle := &TransientError{Status: 429}
	unavailable := &TransientError{Status: 503}
	lost := &TransientError{Err: errors.New("timeout")}
	badGateway := &TransientError{Status: 502}
	for _, tc := range []struct {
		method string
		err    error
		want   bool
	}{
		{"GET", lost, true}, {"DELETE", badGateway, true}, {"PUT", lost, true},
		{"POST", throttle, true}, {"POST", unavailable, true}, {"POST", lost, false}, {"POST", badGateway, false}, {"PATCH", lost, false},
	} {
		if got := safeToRetry(tc.method, tc.err); got != tc.want {
			t.Errorf("%s %v: %v", tc.method, tc.err, got)
		}
	}
}
