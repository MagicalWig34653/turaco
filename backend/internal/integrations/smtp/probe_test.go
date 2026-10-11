package smtp

import (
	"context"
	"strings"
	"testing"
)

func TestProbeConnectsAndGreetsWithoutAuthOrMail(t *testing.T) {
	cert, pool := newCert(t, "localhost")
	s := &fakeServer{cert: cert, offerTLS: true, authUser: "u", authPass: "p"}
	startServer(t, s)
	if err := Probe(context.Background(), newMailer(t, s, pool, StartTLS)); err != nil {
		t.Fatalf("probe: %v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sawAuth || s.data != "" || s.from != "" || s.to != "" {
		t.Errorf("probe must neither authenticate nor address a mail: auth=%v from=%q to=%q", s.sawAuth, s.from, s.to)
	}
	joined := strings.ToUpper(strings.Join(s.commands, ","))
	if !strings.Contains(joined, "EHLO") || strings.Contains(joined, "MAIL") || strings.Contains(joined, "DATA") || strings.Contains(joined, "AUTH") {
		t.Errorf("commands = %v", s.commands)
	}
}

func TestProbeReportsMissingStartTLSAndUnreachableRelay(t *testing.T) {
	cert, pool := newCert(t, "localhost")
	s := &fakeServer{cert: cert, offerTLS: false}
	startServer(t, s)
	if err := Probe(context.Background(), newMailer(t, s, pool, StartTLS)); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("relay without STARTTLS: %v", err)
	}

	down := &fakeServer{}
	startServer(t, down)
	m := newMailer(t, down, nil, None)
	_ = down.ln.Close()
	if err := Probe(context.Background(), m); err == nil {
		t.Error("unreachable relay must fail the probe")
	}
}
