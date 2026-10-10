// Package clamav is the ClamAV adapter of the platform virus scan port (ADR-0037). It speaks the clamd INSTREAM
// protocol over TCP to a daemon in its own container: the content is streamed in length-prefixed chunks, every I/O
// step has a deadline and the stream size is capped on the Turaco side as well.
package clamav

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/attachments"
)

const (
	chunkSize = 64 << 10
	// maxReply bounds the daemon's answer line.
	maxReply = 4096
)

// Config configures the client.
type Config struct {
	// Address is host:port of clamd.
	Address string
	// MaxBytes caps the scanned stream; larger content is refused without being sent completely.
	MaxBytes int64
	// DialTimeout (default 5s) bounds connecting; IOTimeout (default 30s) bounds every single read or write, so a
	// stalled daemon is detected while a slow but progressing scan is not cut off.
	DialTimeout time.Duration
	IOTimeout   time.Duration
}

// Client scans streams through clamd. It opens one connection per scan.
type Client struct {
	cfg  Config
	dial func(ctx context.Context, address string, timeout time.Duration) (net.Conn, error)
}

// New validates the configuration.
func New(cfg Config) (*Client, error) {
	if i := strings.LastIndex(cfg.Address, ":"); i <= 0 || i == len(cfg.Address)-1 {
		return nil, errors.New("clamav: address must be host:port")
	}
	if cfg.MaxBytes <= 0 {
		return nil, errors.New("clamav: max bytes must be positive")
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 5 * time.Second
	}
	if cfg.IOTimeout <= 0 {
		cfg.IOTimeout = 30 * time.Second
	}
	return &Client{cfg: cfg, dial: func(ctx context.Context, address string, timeout time.Duration) (net.Conn, error) {
		d := net.Dialer{Timeout: timeout}
		return d.DialContext(ctx, "tcp", address)
	}}, nil
}

// ErrRefused is wrapped by errors for content clamd refused to scan (for example its stream size limit). Such an
// error is permanent for the content, unlike ErrScannerUnavailable.
var ErrRefused = errors.New("clamav: scan refused")

func (c *Client) unavailable(err error) error {
	return fmt.Errorf("%w: %v", attachments.ErrScannerUnavailable, err)
}

// Ping checks that the daemon answers PING with PONG.
func (c *Client) Ping(ctx context.Context) error {
	conn, err := c.connect(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := c.write(conn, []byte("zPING\x00")); err != nil {
		return c.unavailable(err)
	}
	reply, err := c.readReply(conn)
	if err != nil {
		return c.unavailable(err)
	}
	if reply != "PONG" {
		return c.unavailable(fmt.Errorf("unexpected ping reply %q", reply))
	}
	return nil
}

// Scan implements attachments.Scanner.
func (c *Client) Scan(ctx context.Context, r io.Reader) (attachments.Verdict, error) {
	conn, err := c.connect(ctx)
	if err != nil {
		return attachments.Verdict{}, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	if err := c.write(conn, []byte("zINSTREAM\x00")); err != nil {
		return attachments.Verdict{}, c.unavailable(err)
	}
	buf := make([]byte, 4+chunkSize)
	var total int64
	for {
		n, rerr := r.Read(buf[4:])
		if n > 0 {
			total += int64(n)
			if total > c.cfg.MaxBytes {
				return attachments.Verdict{}, fmt.Errorf("%w: content exceeds %d bytes", ErrRefused, c.cfg.MaxBytes)
			}
			binary.BigEndian.PutUint32(buf, uint32(n))
			if err := c.write(conn, buf[:4+n]); err != nil {
				// clamd answers "size limit exceeded. ERROR" and closes: prefer its reply over the broken pipe.
				if reply, e := c.readReply(conn); e == nil {
					return c.parse(reply)
				}
				return attachments.Verdict{}, c.unavailable(err)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return attachments.Verdict{}, rerr // a source failure is not a scanner failure
		}
	}
	if err := c.write(conn, []byte{0, 0, 0, 0}); err != nil {
		return attachments.Verdict{}, c.unavailable(err)
	}
	reply, err := c.readReply(conn)
	if err != nil {
		if ctx.Err() != nil {
			return attachments.Verdict{}, ctx.Err()
		}
		return attachments.Verdict{}, c.unavailable(err)
	}
	return c.parse(reply)
}

func (c *Client) parse(reply string) (attachments.Verdict, error) {
	reply = strings.TrimSpace(strings.TrimPrefix(reply, "stream:"))
	switch {
	case reply == "OK":
		return attachments.Verdict{Clean: true}, nil
	case strings.HasSuffix(reply, " FOUND"):
		return attachments.Verdict{Signature: strings.TrimSpace(strings.TrimSuffix(reply, " FOUND"))}, nil
	case strings.HasSuffix(reply, "ERROR"):
		return attachments.Verdict{}, fmt.Errorf("%w: %s", ErrRefused, strings.TrimSpace(strings.TrimSuffix(reply, "ERROR")))
	}
	return attachments.Verdict{}, c.unavailable(fmt.Errorf("unexpected reply %q", reply))
}

func (c *Client) connect(ctx context.Context) (net.Conn, error) {
	conn, err := c.dial(ctx, c.cfg.Address, c.cfg.DialTimeout)
	if err != nil {
		return nil, c.unavailable(err)
	}
	return conn, nil
}

func (c *Client) write(conn net.Conn, b []byte) error {
	_ = conn.SetWriteDeadline(time.Now().Add(c.cfg.IOTimeout))
	_, err := conn.Write(b)
	return err
}

// readReply reads one NUL- or newline-terminated reply.
func (c *Client) readReply(conn net.Conn) (string, error) {
	var out []byte
	one := make([]byte, 1)
	for len(out) < maxReply {
		_ = conn.SetReadDeadline(time.Now().Add(c.cfg.IOTimeout))
		if _, err := conn.Read(one); err != nil {
			if len(out) > 0 && errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}
		if one[0] == 0 || one[0] == '\n' {
			break
		}
		out = append(out, one[0])
	}
	return string(out), nil
}
