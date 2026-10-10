package clamav

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/attachments"
)

// fakeClamd answers one INSTREAM or PING command per connection.
type fakeClamd struct {
	ln       net.Listener
	reply    string
	limit    int // when > 0, answer with the size-limit error after this many bytes and close
	received chan []byte
}

func startFake(t *testing.T, reply string) *fakeClamd {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback listener: %v", err)
	}
	f := &fakeClamd{ln: ln, reply: reply, received: make(chan []byte, 4)}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *fakeClamd) serve(conn net.Conn) {
	defer conn.Close()
	cmd := make([]byte, 0, 16)
	one := make([]byte, 1)
	for {
		if _, err := conn.Read(one); err != nil {
			return
		}
		if one[0] == 0 {
			break
		}
		cmd = append(cmd, one[0])
	}
	if string(cmd) == "zPING" {
		_, _ = conn.Write([]byte("PONG\x00"))
		return
	}
	if string(cmd) != "zINSTREAM" {
		return
	}
	var got []byte
	for {
		var l [4]byte
		if _, err := io.ReadFull(conn, l[:]); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(l[:])
		if n == 0 {
			break
		}
		chunk := make([]byte, n)
		if _, err := io.ReadFull(conn, chunk); err != nil {
			return
		}
		got = append(got, chunk...)
		if f.limit > 0 && len(got) > f.limit {
			_, _ = conn.Write([]byte("INSTREAM size limit exceeded. ERROR\x00"))
			return
		}
	}
	f.received <- got
	_, _ = conn.Write([]byte(f.reply + "\x00"))
}

func client(t *testing.T, f *fakeClamd, max int64) *Client {
	t.Helper()
	c, err := New(Config{Address: f.ln.Addr().String(), MaxBytes: max, IOTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCleanAndChunking(t *testing.T) {
	f := startFake(t, "stream: OK")
	c := client(t, f, 10<<20)
	content := bytes.Repeat([]byte("0123456789"), 50000) // 500 kB: several chunks
	v, err := c.Scan(context.Background(), bytes.NewReader(content))
	if err != nil || !v.Clean {
		t.Fatalf("verdict %+v err %v", v, err)
	}
	if !bytes.Equal(<-f.received, content) {
		t.Fatal("daemon received different bytes")
	}
}

func TestFoundReturnsSignature(t *testing.T) {
	f := startFake(t, "stream: Win.Test.EICAR_HDB-1 FOUND")
	v, err := client(t, f, 1<<20).Scan(context.Background(), strings.NewReader("X5O!P%@AP"))
	if err != nil || v.Clean || v.Signature != "Win.Test.EICAR_HDB-1" {
		t.Fatalf("verdict %+v err %v", v, err)
	}
}

func TestEmptyStream(t *testing.T) {
	f := startFake(t, "stream: OK")
	v, err := client(t, f, 1<<20).Scan(context.Background(), strings.NewReader(""))
	if err != nil || !v.Clean {
		t.Fatalf("verdict %+v err %v", v, err)
	}
}

func TestErrorReplyIsRefusal(t *testing.T) {
	f := startFake(t, "stream: Something broke ERROR")
	_, err := client(t, f, 1<<20).Scan(context.Background(), strings.NewReader("x"))
	if !errors.Is(err, ErrRefused) || errors.Is(err, attachments.ErrScannerUnavailable) {
		t.Fatalf("got %v", err)
	}
}

func TestDaemonSizeLimitIsRefusal(t *testing.T) {
	f := startFake(t, "stream: OK")
	f.limit = 100 << 10
	_, err := client(t, f, 10<<20).Scan(context.Background(), bytes.NewReader(make([]byte, 2<<20)))
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("got %v", err)
	}
}

func TestClientSideSizeCap(t *testing.T) {
	f := startFake(t, "stream: OK")
	_, err := client(t, f, 1000).Scan(context.Background(), bytes.NewReader(make([]byte, 5000)))
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("got %v", err)
	}
}

func TestUnreachableDaemon(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()
	c, _ := New(Config{Address: addr, MaxBytes: 1 << 20, DialTimeout: time.Second})
	if _, err := c.Scan(context.Background(), strings.NewReader("x")); !errors.Is(err, attachments.ErrScannerUnavailable) {
		t.Fatalf("scan: %v", err)
	}
	if err := c.Ping(context.Background()); !errors.Is(err, attachments.ErrScannerUnavailable) {
		t.Fatalf("ping: %v", err)
	}
}

func TestStalledDaemonTimesOut(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { time.Sleep(3 * time.Second); _ = conn.Close() }() // accepts and never answers
		}
	}()
	c, _ := New(Config{Address: ln.Addr().String(), MaxBytes: 1 << 20, IOTimeout: 200 * time.Millisecond})
	start := time.Now()
	_, err = c.Scan(context.Background(), strings.NewReader("x"))
	if !errors.Is(err, attachments.ErrScannerUnavailable) || time.Since(start) > 2*time.Second {
		t.Fatalf("err %v after %v", err, time.Since(start))
	}
}

type failingReader struct{ err error }

func (f failingReader) Read([]byte) (int, error) { return 0, f.err }

func TestSourceErrorIsNotAScannerError(t *testing.T) {
	f := startFake(t, "stream: OK")
	boom := errors.New("decrypt failed")
	_, err := client(t, f, 1<<20).Scan(context.Background(), failingReader{boom})
	if !errors.Is(err, boom) || errors.Is(err, attachments.ErrScannerUnavailable) {
		t.Fatalf("got %v", err)
	}
}

func TestPing(t *testing.T) {
	f := startFake(t, "")
	if err := client(t, f, 1<<20).Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestNewValidation(t *testing.T) {
	for _, c := range []Config{{Address: "nohost", MaxBytes: 1}, {Address: "h:", MaxBytes: 1}, {Address: "h:3310"}} {
		if _, err := New(c); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
}
