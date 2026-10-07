package httpx

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A handler that aborts a streamed response on purpose (http.ErrAbortHandler) is not turned into a 500 JSON body
// appended to the stream: the middleware passes the abort on to net/http.
func TestMiddlewarePassesAbortHandlerOn(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := Middleware(logger, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }))
	defer func() {
		if r := recover(); r != http.ErrAbortHandler {
			t.Fatalf("recovered %v, want http.ErrAbortHandler", r)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	t.Fatal("middleware swallowed the abort")
}
