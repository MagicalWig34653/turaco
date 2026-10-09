package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// Pagination defaults shared by list endpoints.
const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// RequestID returns the server-generated request/correlation ID that
// Middleware set on the response.
func RequestID(w http.ResponseWriter) string { return w.Header().Get("X-Request-ID") }

// WriteError writes the standard error envelope. message must be a fixed,
// user-safe text, never an internal error.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	JSON(w, status, ErrorEnvelope{Error: APIError{Code: code, Message: message, RequestID: RequestID(w)}})
}

// WriteErrorDetails is WriteError with machine-readable details.
func WriteErrorDetails(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	JSON(w, status, ErrorEnvelope{Error: APIError{Code: code, Message: message, RequestID: RequestID(w), Details: details}})
}

// NoStore marks responses as not cacheable (authenticated API data).
func NoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// ErrInvalidJSON is returned by DecodeJSON for any malformed, oversized,
// unknown-field or trailing-data body. Callers answer 400 with their own code.
var ErrInvalidJSON = errors.New("httpx: invalid JSON body")

// DecodeJSON strictly decodes a single JSON object of at most maxBytes into
// dst: unknown fields, trailing data and oversized bodies are rejected.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	body := http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidJSON, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing data", ErrInvalidJSON)
	}
	return nil
}

// ErrInvalidLimit is returned by ParseLimit for a non-numeric or non-positive
// limit query parameter.
var ErrInvalidLimit = errors.New("httpx: invalid limit")

// ParseLimit reads the "limit" query parameter: absent means DefaultLimit,
// values above MaxLimit are clamped.
func ParseLimit(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return DefaultLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, ErrInvalidLimit
	}
	if n > MaxLimit {
		n = MaxLimit
	}
	return n, nil
}
