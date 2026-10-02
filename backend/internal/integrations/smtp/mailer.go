// Package smtp is the outbound email adapter (ADR-0024): it sends one
// multipart HTML/text message through an SMTP relay using only the standard
// library. Callers see Message, Mailer and PermanentError; nothing of
// net/smtp leaks out.
package smtp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// Security selects how the connection to the relay is protected.
type Security string

const (
	// StartTLS connects in clear text and upgrades with STARTTLS before any
	// credential or message is sent (the default; usually port 587).
	StartTLS Security = "starttls"
	// ImplicitTLS wraps the connection in TLS from the start (port 465).
	ImplicitTLS Security = "tls"
	// None sends in clear text. Development relays only; the configuration
	// loader refuses it outside APP_ENV=development.
	None Security = "none"
)

// Config configures a Mailer. Certificate verification is never disabled.
type Config struct {
	Host     string
	Port     int
	Security Security
	Username string
	Password string
	// From is the envelope and header sender, for example "Turaco <turaco@example.org>".
	From string
	// Timeout bounds one send (connect, dialogue and transfer). Default 30s.
	Timeout time.Duration
	// RootCAs are additional trusted CAs; nil uses the system pool only.
	RootCAs *x509.CertPool
}

// Message is one email to one recipient.
type Message struct {
	To      string // an address, optionally with a display name
	Subject string
	Text    string
	HTML    string
}

// Mailer sends messages.
type Mailer interface {
	Send(ctx context.Context, m Message) error
}

// PermanentError means the relay rejected the message for good (5xx) or the
// message is unusable; retrying cannot help.
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// IsPermanent reports whether err is a PermanentError.
func IsPermanent(err error) bool {
	var p *PermanentError
	return errors.As(err, &p)
}

type client struct {
	cfg  Config
	from *mail.Address
	now  func() time.Time
}

// New validates cfg and returns a Mailer.
func New(cfg Config) (Mailer, error) {
	if cfg.Host == "" || strings.ContainsAny(cfg.Host, " \r\n/") {
		return nil, errors.New("smtp: host is required")
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return nil, errors.New("smtp: port must be 1-65535")
	}
	switch cfg.Security {
	case StartTLS, ImplicitTLS, None:
	default:
		return nil, fmt.Errorf("smtp: unknown security mode %q", cfg.Security)
	}
	from, err := parseAddress(cfg.From)
	if err != nil {
		return nil, fmt.Errorf("smtp: from address: %w", err)
	}
	if (cfg.Username == "") != (cfg.Password == "") {
		return nil, errors.New("smtp: username and password go together")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &client{cfg: cfg, from: from, now: time.Now}, nil
}

func parseAddress(s string) (*mail.Address, error) {
	if strings.ContainsAny(s, "\r\n\x00") {
		return nil, errors.New("contains control characters")
	}
	a, err := mail.ParseAddress(s)
	if err != nil {
		return nil, err
	}
	return a, nil
}

// Send delivers m. Network and 4xx failures are returned as ordinary errors
// (retryable); 5xx rejections and unusable messages are PermanentError.
func (c *client) Send(ctx context.Context, m Message) error {
	to, err := parseAddress(m.To)
	if err != nil {
		return &PermanentError{Err: fmt.Errorf("smtp: recipient address: %w", err)}
	}
	body, err := buildMessage(c.from, to, m, c.now())
	if err != nil {
		return &PermanentError{Err: err}
	}

	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	conn, err := c.dial(ctx)
	if err != nil {
		return fmt.Errorf("smtp: connect: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	// Closing the connection unblocks a dialogue stuck beyond the deadline
	// when the caller cancels.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	sc, err := smtp.NewClient(conn, c.cfg.Host)
	if err != nil {
		return fmt.Errorf("smtp: greeting: %w", classify(err))
	}
	defer func() { _ = sc.Close() }()
	if c.cfg.Security == StartTLS {
		if ok, _ := sc.Extension("STARTTLS"); !ok {
			return fmt.Errorf("smtp: relay does not offer STARTTLS")
		}
		if err := sc.StartTLS(c.tlsConfig()); err != nil {
			return fmt.Errorf("smtp: starttls: %w", classify(err))
		}
	}
	if c.cfg.Username != "" {
		// PlainAuth refuses to send credentials over an unencrypted
		// connection (except to localhost), which is the behaviour we want.
		if err := sc.Auth(smtp.PlainAuth("", c.cfg.Username, c.cfg.Password, c.cfg.Host)); err != nil {
			return fmt.Errorf("smtp: auth: %w", classify(err))
		}
	}
	if err := sc.Mail(c.from.Address); err != nil {
		return fmt.Errorf("smtp: mail from: %w", classify(err))
	}
	if err := sc.Rcpt(to.Address); err != nil {
		return fmt.Errorf("smtp: rcpt to: %w", classify(err))
	}
	w, err := sc.Data()
	if err != nil {
		return fmt.Errorf("smtp: data: %w", classify(err))
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("smtp: write: %w", classify(err))
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp: end of data: %w", classify(err))
	}
	_ = sc.Quit()
	return nil
}

func (c *client) tlsConfig() *tls.Config {
	return &tls.Config{ServerName: c.cfg.Host, MinVersion: tls.VersionTLS12, RootCAs: c.cfg.RootCAs}
}

func (c *client) dial(ctx context.Context) (net.Conn, error) {
	addr := net.JoinHostPort(c.cfg.Host, strconv.Itoa(c.cfg.Port))
	d := &net.Dialer{}
	if c.cfg.Security == ImplicitTLS {
		td := &tls.Dialer{NetDialer: d, Config: c.tlsConfig()}
		return td.DialContext(ctx, "tcp", addr)
	}
	return d.DialContext(ctx, "tcp", addr)
}

// classify marks 5xx replies permanent and everything else retryable.
func classify(err error) error {
	var te *textproto.Error
	if errors.As(err, &te) && te.Code >= 500 && te.Code < 600 {
		return &PermanentError{Err: err}
	}
	return err
}
