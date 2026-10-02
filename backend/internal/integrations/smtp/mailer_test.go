package smtp

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"math/big"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServer is a minimal SMTP relay: EHLO, STARTTLS, AUTH PLAIN, MAIL, RCPT,
// DATA, QUIT. Failures are injected per command.
type fakeServer struct {
	t        *testing.T
	ln       net.Listener
	cert     tls.Certificate
	implicit bool
	offerTLS bool
	authUser string
	authPass string
	failAt   map[string]string // command -> full reply line

	mu       sync.Mutex
	sawAuth  bool
	sawClear bool // AUTH or DATA seen on an unencrypted connection
	from, to string
	data     string
	commands []string
}

func newCert(t *testing.T, host string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: []string{host}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

func startServer(t *testing.T, s *fakeServer) {
	t.Helper()
	s.t = t
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if s.implicit {
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{s.cert}})
	}
	s.ln = ln
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
}

func (s *fakeServer) port() int { return s.ln.Addr().(*net.TCPAddr).Port }

func (s *fakeServer) record(cmd string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commands = append(s.commands, cmd)
}

func (s *fakeServer) serve(c net.Conn) {
	defer func() { _ = c.Close() }()
	encrypted := s.implicit
	r := bufio.NewReader(c)
	reply := func(line string) { _, _ = fmt.Fprintf(c, "%s\r\n", line) }
	reply("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(strings.Fields(line + " x")[0])
		s.record(verb)
		if f, ok := s.failAt[verb]; ok {
			reply(f)
			if strings.HasPrefix(f, "4") || strings.HasPrefix(f, "5") {
				continue
			}
		}
		switch verb {
		case "EHLO":
			reply("250-fake")
			if s.offerTLS && !encrypted {
				reply("250-STARTTLS")
			}
			reply("250 AUTH PLAIN")
		case "STARTTLS":
			reply("220 go ahead")
			tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{s.cert}})
			if err := tc.Handshake(); err != nil {
				return
			}
			c, r, encrypted = tc, bufio.NewReader(tc), true
			reply = func(l string) { _, _ = fmt.Fprintf(tc, "%s\r\n", l) }
		case "AUTH":
			s.mu.Lock()
			s.sawAuth = true
			if !encrypted {
				s.sawClear = true
			}
			s.mu.Unlock()
			f := strings.Fields(line)
			raw, _ := base64.StdEncoding.DecodeString(f[len(f)-1])
			parts := strings.Split(string(raw), "\x00")
			if len(parts) == 3 && parts[1] == s.authUser && parts[2] == s.authPass {
				reply("235 ok")
			} else {
				reply("535 bad credentials")
			}
		case "MAIL":
			s.mu.Lock()
			s.from = line
			s.mu.Unlock()
			reply("250 ok")
		case "RCPT":
			s.mu.Lock()
			s.to = line
			s.mu.Unlock()
			reply("250 ok")
		case "DATA":
			reply("354 go")
			var sb strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				sb.WriteString(l)
			}
			s.mu.Lock()
			s.data = sb.String()
			if !encrypted {
				s.sawClear = true
			}
			s.mu.Unlock()
			reply("250 queued")
		case "QUIT":
			reply("221 bye")
			return
		default:
			reply("250 ok")
		}
	}
}

func newMailer(t *testing.T, s *fakeServer, pool *x509.CertPool, sec Security) Mailer {
	t.Helper()
	m, err := New(Config{
		Host: "localhost", Port: s.port(), Security: sec, From: "Turaco <turaco@example.org>",
		Username: s.authUser, Password: s.authPass, Timeout: 5 * time.Second, RootCAs: pool,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

var msg = Message{To: "Ada Lovelace <ada@example.org>", Subject: "Task assigned: Toner ersetzen für Müller", Text: "plain body", HTML: "<p>html body</p>"}

func TestSendWithStartTLSAndAuth(t *testing.T) {
	cert, pool := newCert(t, "localhost")
	s := &fakeServer{cert: cert, offerTLS: true, authUser: "u", authPass: "p"}
	startServer(t, s)
	if err := newMailer(t, s, pool, StartTLS).Send(context.Background(), msg); err != nil {
		t.Fatalf("send: %v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sawClear {
		t.Error("credentials or message crossed an unencrypted connection")
	}
	if !s.sawAuth || !strings.Contains(s.from, "turaco@example.org") || !strings.Contains(s.to, "ada@example.org") {
		t.Errorf("envelope: auth=%v from=%q to=%q", s.sawAuth, s.from, s.to)
	}
	parsed, err := mail.ReadMessage(strings.NewReader(s.data))
	if err != nil {
		t.Fatal(err)
	}
	dec := new(mime.WordDecoder)
	subject, err := dec.DecodeHeader(parsed.Header.Get("Subject"))
	if err != nil || subject != msg.Subject {
		t.Errorf("subject = %q %v", subject, err)
	}
	if parsed.Header.Get("Auto-Submitted") != "auto-generated" || parsed.Header.Get("Message-ID") == "" || parsed.Header.Get("MIME-Version") != "1.0" {
		t.Errorf("headers = %v", parsed.Header)
	}
	mediaType, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/alternative" {
		t.Fatalf("content type = %q %v", mediaType, err)
	}
	mr := multipart.NewReader(parsed.Body, params["boundary"])
	var got []string
	for {
		p, err := mr.NextPart()
		if err != nil {
			break
		}
		b := new(strings.Builder)
		buf := make([]byte, 512)
		qr := quotedprintable.NewReader(p)
		for {
			n, err := qr.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		got = append(got, p.Header.Get("Content-Type")+"|"+strings.TrimSpace(b.String()))
	}
	if len(got) != 2 || !strings.HasSuffix(got[0], "|plain body") || !strings.HasSuffix(got[1], "|<p>html body</p>") {
		t.Errorf("parts = %q", got)
	}
}

func TestSendWithImplicitTLS(t *testing.T) {
	cert, pool := newCert(t, "localhost")
	s := &fakeServer{cert: cert, implicit: true}
	startServer(t, s)
	if err := newMailer(t, s, pool, ImplicitTLS).Send(context.Background(), msg); err != nil {
		t.Fatalf("send: %v", err)
	}
}

func TestSendWithoutTLSInDevelopmentMode(t *testing.T) {
	s := &fakeServer{}
	startServer(t, s)
	if err := newMailer(t, s, nil, None).Send(context.Background(), msg); err != nil {
		t.Fatalf("send: %v", err)
	}
}

func TestStartTLSIsRequiredNotOptional(t *testing.T) {
	cert, pool := newCert(t, "localhost")
	s := &fakeServer{cert: cert, offerTLS: false, authUser: "u", authPass: "p"}
	startServer(t, s)
	err := newMailer(t, s, pool, StartTLS).Send(context.Background(), msg)
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("err = %v, want a refusal to continue without STARTTLS", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sawAuth || s.data != "" {
		t.Error("credentials or message were sent although the relay offered no STARTTLS")
	}
}

func TestUntrustedCertificateIsRejected(t *testing.T) {
	cert, _ := newCert(t, "localhost")
	_, otherPool := newCert(t, "localhost")
	s := &fakeServer{cert: cert, offerTLS: true, authUser: "u", authPass: "p"}
	startServer(t, s)
	if err := newMailer(t, s, otherPool, StartTLS).Send(context.Background(), msg); err == nil {
		t.Fatal("a certificate from an untrusted CA must fail the handshake")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sawAuth {
		t.Error("credentials were sent after a failed handshake")
	}
}

func TestErrorClassification(t *testing.T) {
	cert, pool := newCert(t, "localhost")
	cases := []struct {
		name      string
		failAt    map[string]string
		permanent bool
	}{
		{"mailbox unavailable is permanent", map[string]string{"RCPT": "550 no such user"}, true},
		{"bad credentials are permanent", map[string]string{"AUTH": "535 bad credentials"}, true},
		{"greylisting is retryable", map[string]string{"RCPT": "451 try later"}, false},
		{"mailbox full is retryable", map[string]string{"DATA": "452 insufficient storage"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &fakeServer{cert: cert, offerTLS: true, authUser: "u", authPass: "p", failAt: c.failAt}
			startServer(t, s)
			err := newMailer(t, s, pool, StartTLS).Send(context.Background(), msg)
			if err == nil || IsPermanent(err) != c.permanent {
				t.Errorf("err = %v, permanent = %v, want permanent = %v", err, IsPermanent(err), c.permanent)
			}
		})
	}
}

func TestUnreachableRelayIsRetryable(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	m, err := New(Config{Host: "127.0.0.1", Port: port, Security: None, From: "turaco@example.org", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	err = m.Send(context.Background(), msg)
	if err == nil || IsPermanent(err) {
		t.Errorf("err = %v, want a retryable error", err)
	}
}

func TestSlowRelayHitsTheTimeout(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer func() { _ = c.Close() }() // accepts and never speaks
		}
	}()
	m, _ := New(Config{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, Security: None, From: "turaco@example.org", Timeout: 200 * time.Millisecond})
	start := time.Now()
	if err := m.Send(context.Background(), msg); err == nil {
		t.Fatal("want an error")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %s, the timeout was not enforced", d)
	}
}

func TestHeaderInjectionIsNeutralized(t *testing.T) {
	s := &fakeServer{}
	startServer(t, s)
	m := newMailer(t, s, nil, None)
	evil := Message{To: "ada@example.org", Subject: "Hi\r\nBcc: attacker@example.org", Text: "t", HTML: "<p>h</p>"}
	if err := m.Send(context.Background(), evil); err != nil {
		t.Fatalf("send: %v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	head := strings.SplitN(s.data, "\r\n\r\n", 2)[0]
	for _, line := range strings.Split(head, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "bcc:") {
			t.Fatalf("injected header: %q", line)
		}
	}
	// Recipient addresses with control characters are refused before any connection.
	for _, to := range []string{"ada@example.org\r\nBcc: x@example.org", "ada@example.org\nRCPT TO:<x@example.org>", ""} {
		err := m.Send(context.Background(), Message{To: to, Subject: "s", Text: "t", HTML: "<p>h</p>"})
		if !IsPermanent(err) {
			t.Errorf("to=%q: err = %v, want a permanent refusal", to, err)
		}
	}
}

func TestUnusableMessagesArePermanentFailures(t *testing.T) {
	s := &fakeServer{}
	startServer(t, s)
	m := newMailer(t, s, nil, None)
	for name, in := range map[string]Message{
		"no subject": {To: "a@example.org", Subject: " \r\n", Text: "t", HTML: "h"},
		"no text":    {To: "a@example.org", Subject: "s", HTML: "h"},
		"no html":    {To: "a@example.org", Subject: "s", Text: "t"},
	} {
		if err := m.Send(context.Background(), in); !IsPermanent(err) {
			t.Errorf("%s: err = %v, want permanent", name, err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.commands) != 0 {
		t.Errorf("unusable messages reached the relay: %v", s.commands)
	}
}

func TestNewValidatesConfig(t *testing.T) {
	good := Config{Host: "mail.example.org", Port: 587, Security: StartTLS, From: "turaco@example.org"}
	if _, err := New(good); err != nil {
		t.Fatalf("good config: %v", err)
	}
	for name, mutate := range map[string]func(*Config){
		"no host":           func(c *Config) { c.Host = "" },
		"host with space":   func(c *Config) { c.Host = "a b" },
		"bad port":          func(c *Config) { c.Port = 0 },
		"unknown security":  func(c *Config) { c.Security = "ssl3" },
		"bad from":          func(c *Config) { c.From = "not an address" },
		"from with newline": func(c *Config) { c.From = "a@example.org\r\nBcc: b@example.org" },
		"user without pass": func(c *Config) { c.Username = "u" },
		"pass without user": func(c *Config) { c.Password = "p" },
	} {
		c := good
		mutate(&c)
		if _, err := New(c); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestContextCancellationStopsSend(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err == nil {
			defer func() { _ = c.Close() }()
			time.Sleep(3 * time.Second)
		}
	}()
	m, _ := New(Config{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, Security: None, From: "turaco@example.org", Timeout: 30 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	start := time.Now()
	err := m.Send(ctx, msg)
	if err == nil || time.Since(start) > 2*time.Second {
		t.Errorf("err = %v after %s, want a prompt failure on cancel", err, time.Since(start))
	}
}
