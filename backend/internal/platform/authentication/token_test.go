package authentication

import (
	"bytes"
	"testing"
)

func TestNewTokenFormatAndUniqueness(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		tok, err := newToken()
		if err != nil {
			t.Fatal(err)
		}
		if !validTokenFormat(tok) {
			t.Fatalf("token %q has invalid format", tok)
		}
		if seen[tok] {
			t.Fatal("duplicate token")
		}
		seen[tok] = true
	}
}

func TestHashToken(t *testing.T) {
	h := HashToken("abc")
	if len(h) != 32 {
		t.Fatalf("len = %d", len(h))
	}
	if !bytes.Equal(h, HashToken("abc")) || bytes.Equal(h, HashToken("abd")) {
		t.Fatal("hash must be deterministic and input-sensitive")
	}
	if HashToken("abc")[0] != 0xba {
		t.Fatal("not SHA-256")
	}
}

func TestValidTokenFormat(t *testing.T) {
	good, _ := newToken()
	tests := map[string]bool{
		good:              true,
		"":                false,
		"short":           false,
		good + "A":        false,
		good[:42] + "!":   false,
		good[:42] + "=":   false,
		"\x00" + good[1:]: false,
	}
	for in, want := range tests {
		if got := validTokenFormat(in); got != want {
			t.Errorf("validTokenFormat(%q) = %v, want %v", in, got, want)
		}
	}
}
