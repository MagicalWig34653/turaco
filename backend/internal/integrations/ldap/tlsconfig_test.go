package ldap

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeSelfSignedCA writes a self-signed CA certificate valid for the DNS name
// and returns the PEM file path and the certificate.
func writeSelfSignedCA(t *testing.T, dnsName string) (string, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Turaco test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{dnsName},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, cert
}

func TestBuildTLSConfigDefaults(t *testing.T) {
	cfg, err := buildTLSConfig("ldaps://dc.example.test:636", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.InsecureSkipVerify {
		t.Fatal("InsecureSkipVerify must never be set")
	}
	if cfg.ServerName != "dc.example.test" {
		t.Errorf("ServerName = %q, want host without port", cfg.ServerName)
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %x, want TLS 1.2", cfg.MinVersion)
	}
	if cfg.RootCAs == nil {
		t.Error("RootCAs must be an explicit pool")
	}
}

func TestBuildTLSConfigServerNameFromURL(t *testing.T) {
	for url, want := range map[string]string{
		"ldaps://dc.example.test":         "dc.example.test",
		"ldap://dc.example.test:389":      "dc.example.test",
		"ldaps://[2001:db8::1]:636":       "2001:db8::1",
		"ldaps://DC1.Corp.Example.Test:1": "DC1.Corp.Example.Test",
	} {
		cfg, err := buildTLSConfig(url, "")
		if err != nil {
			t.Fatalf("%s: %v", url, err)
		}
		if cfg.ServerName != want {
			t.Errorf("%s: ServerName = %q, want %q", url, cfg.ServerName, want)
		}
	}
	for _, bad := range []string{"", "ldaps://", "://x", "ldaps://:636"} {
		if _, err := buildTLSConfig(bad, ""); err == nil {
			t.Errorf("buildTLSConfig(%q) accepted a URL without host", bad)
		}
	}
}

func TestBuildTLSConfigLoadsCAFile(t *testing.T) {
	caFile, cert := writeSelfSignedCA(t, "dc.example.test")
	cfg, err := buildTLSConfig("ldaps://dc.example.test:636", caFile)
	if err != nil {
		t.Fatal(err)
	}
	opts := x509.VerifyOptions{Roots: cfg.RootCAs, DNSName: cfg.ServerName, CurrentTime: time.Now()}
	if _, err := cert.Verify(opts); err != nil {
		t.Fatalf("certificate from CA file is not trusted by the configured pool: %v", err)
	}

	// Without the CA file the same certificate must not verify.
	plain, err := buildTLSConfig("ldaps://dc.example.test:636", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: plain.RootCAs, DNSName: "dc.example.test"}); err == nil {
		t.Fatal("self-signed certificate verified without the CA file")
	}
	// A server name mismatch must fail even with the CA trusted.
	if _, err := cert.Verify(x509.VerifyOptions{Roots: cfg.RootCAs, DNSName: "other.example.test"}); err == nil {
		t.Fatal("certificate verified for the wrong server name")
	}
}

func TestBuildTLSConfigRejectsBadCAFile(t *testing.T) {
	dir := t.TempDir()
	notPEM := filepath.Join(dir, "garbage.pem")
	if err := os.WriteFile(notPEM, []byte("this is not a certificate, TopSecretContent"), 0o600); err != nil {
		t.Fatal(err)
	}
	emptyPEM := filepath.Join(dir, "empty.pem")
	if err := os.WriteFile(emptyPEM, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	badBlock := filepath.Join(dir, "badblock.pem")
	if err := os.WriteFile(badBlock, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("junk")}), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"not pem": notPEM, "empty": emptyPEM, "invalid certificate": badBlock, "missing": filepath.Join(dir, "missing.pem"),
	} {
		_, err := buildTLSConfig("ldaps://dc.example.test", path)
		if err == nil {
			t.Errorf("%s: expected error", name)
			continue
		}
		if strings.Contains(err.Error(), "TopSecretContent") {
			t.Errorf("%s: error leaks file content: %v", name, err)
		}
	}
}

func TestNewSourceBuildsTLSConfigOnce(t *testing.T) {
	caFile, _ := writeSelfSignedCA(t, "dc.example.test")
	cfg := adConfig()
	cfg.CAFile = caFile
	s, err := NewSource(cfg, testBindPassword, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.tlsConfig == nil || s.tlsConfig.InsecureSkipVerify || s.tlsConfig.ServerName != "dc.example.test" || s.tlsConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("tls config = %+v", s.tlsConfig)
	}

	cfg.CAFile = filepath.Join(t.TempDir(), "missing.pem")
	if _, err := NewSource(cfg, testBindPassword, nil); err == nil {
		t.Fatal("NewSource accepted a missing CA file")
	}
}
