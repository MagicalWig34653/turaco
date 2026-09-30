package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

type ErrorEnvelope struct {
	Error APIError `json:"error"`
}

type APIError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"requestId,omitempty"`
}

func JSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func Middleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = time.Now().UTC().Format("20060102T150405.000000000Z07:00")
		}
		w.Header().Set("X-Request-ID", requestID)
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error("http panic", "request_id", requestID, "error", recovered, "stack", string(debug.Stack()))
				JSON(w, http.StatusInternalServerError, ErrorEnvelope{Error: APIError{Code: "platform.internal_error", Message: "An internal error occurred.", RequestID: requestID}})
			}
			logger.Info("http request", "request_id", requestID, "method", r.Method, "path", r.URL.Path, "duration", time.Since(started))
		}()
		next.ServeHTTP(w, r)
	})
}
