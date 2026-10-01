package httpx

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSON(t *testing.T) {
	type in struct {
		A string `json:"a"`
	}
	tests := map[string]struct {
		body string
		ok   bool
	}{
		"valid":         {`{"a":"x"}`, true},
		"unknown field": {`{"a":"x","b":1}`, false},
		"trailing":      {`{"a":"x"}{}`, false},
		"oversized":     {`{"a":"` + strings.Repeat("x", 100) + `"}`, false},
		"malformed":     {`{`, false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var dst in
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
			err := DecodeJSON(httptest.NewRecorder(), r, &dst, 64)
			if tt.ok != (err == nil) || (err != nil && !errors.Is(err, ErrInvalidJSON)) {
				t.Fatalf("err = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

func TestParseLimit(t *testing.T) {
	for raw, want := range map[string]int{"": DefaultLimit, "10": 10, "500": MaxLimit} {
		got, err := ParseLimit(httptest.NewRequest(http.MethodGet, "/?limit="+raw, nil))
		if err != nil || got != want {
			t.Errorf("limit %q = %d, %v; want %d", raw, got, err, want)
		}
	}
	for _, raw := range []string{"0", "-1", "x"} {
		if _, err := ParseLimit(httptest.NewRequest(http.MethodGet, "/?limit="+raw, nil)); !errors.Is(err, ErrInvalidLimit) {
			t.Errorf("limit %q err = %v", raw, err)
		}
	}
}

func TestRequestIDIsAlwaysServerGenerated(t *testing.T) {
	var seen string
	h := Middleware(slog.New(slog.NewTextHandler(io.Discard, nil)), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		seen = RequestID(w)
	}))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Request-ID", "client-chosen")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if seen == "" || seen == "client-chosen" || rec.Header().Get("X-Request-ID") != seen {
		t.Fatalf("request id = %q", seen)
	}
}
