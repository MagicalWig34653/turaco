package httpx

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestIDHandling(t *testing.T) {
	h := Middleware(slog.New(slog.NewTextHandler(io.Discard, nil)), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	tests := []struct {
		name, in string
		keep     bool
	}{
		{"valid kept", "req-1.abc_2", true},
		{"empty replaced", "", false},
		{"too long replaced", strings.Repeat("a", 65), false},
		{"special characters replaced", "a b\"<x>", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.in != "" {
				req.Header.Set("X-Request-ID", tt.in)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			got := rec.Header().Get("X-Request-ID")
			if tt.keep != (got == tt.in) || !validRequestID.MatchString(got) {
				t.Fatalf("request id = %q", got)
			}
		})
	}
}
