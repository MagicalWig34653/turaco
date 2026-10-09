package query

import (
	"errors"
	"net/http"
)

// Error codes returned to API clients.
const (
	CodeInvalidFilter    = "query.invalid_filter"
	CodeTooComplex       = "query.too_complex"
	CodeFieldUnavailable = "query.field_unavailable"
	CodeInvalidCursor    = "query.invalid_cursor"
	CodeUnindexedSort    = "query.unindexed_sort"
	CodeTimeout          = "query.timeout"
	CodeRateLimited      = "query.rate_limited"
)

// Error is a client-facing query error. Messages are fixed texts that never
// echo request values; Path locates the problem in the filter (for example
// "root.children[1]").
type Error struct {
	Code    string
	Message string
	Path    string
	status  int
}

func (e *Error) Error() string {
	if e.Path != "" {
		return e.Code + ": " + e.Message + " (" + e.Path + ")"
	}
	return e.Code + ": " + e.Message
}

// Status is the HTTP status of the error.
func (e *Error) Status() int { return e.status }

func invalid(path, msg string) *Error {
	return &Error{Code: CodeInvalidFilter, Message: msg, Path: path, status: http.StatusBadRequest}
}

func tooComplex(path, msg string) *Error {
	return &Error{Code: CodeTooComplex, Message: msg, Path: path, status: http.StatusUnprocessableEntity}
}

// ErrInvalidCursor is returned for a cursor that is malformed, tampered with
// or issued for a different request or caller.
var ErrInvalidCursor = &Error{Code: CodeInvalidCursor, Message: "The cursor is invalid for this request.", status: http.StatusBadRequest}

// ErrTimeout is returned when the statement timeout cancelled the query.
var ErrTimeout = &Error{Code: CodeTimeout, Message: "The query took too long; narrow the filter.", status: http.StatusServiceUnavailable}

// ErrRateLimited is returned when the caller exceeded the query rate limit.
var ErrRateLimited = &Error{Code: CodeRateLimited, Message: "Too many queries; try again shortly.", status: http.StatusTooManyRequests}

// AsError returns the *Error inside err, if any.
func AsError(err error) (*Error, bool) {
	var qe *Error
	if errors.As(err, &qe) {
		return qe, true
	}
	return nil, false
}
