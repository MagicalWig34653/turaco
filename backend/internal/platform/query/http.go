package query

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

// MaxBodyBytes bounds a POST /<resource>/query body.
const MaxBodyBytes = 24 << 10

// DecodeBody decodes the strict JSON body of a query request.
func DecodeBody(w http.ResponseWriter, r *http.Request) (Request, error) {
	var req Request
	if err := httpx.DecodeJSON(w, r, &req, MaxBodyBytes); err != nil {
		return Request{}, invalid("", "The request body is not a valid query.")
	}
	return req, nil
}

// HasParams reports whether the list request uses any query-engine parameter.
func HasParams(v url.Values) bool {
	for _, k := range []string{"filter", "sort", "search", "count"} {
		if _, ok := v[k]; ok {
			return true
		}
	}
	return false
}

// ParseParams reads the additive engine parameters of a list endpoint:
// filter=<Filter AST JSON>, search=<text>, sort=<field:dir[:nulls],...>,
// count=true plus the endpoint's own cursor and limit.
func ParseParams(v url.Values, cursor string, limit int) (Request, error) {
	req := Request{Cursor: cursor, Limit: limit, Search: v.Get("search")}
	if raw := v.Get("filter"); raw != "" {
		f, err := DecodeFilter([]byte(raw))
		if err != nil {
			return Request{}, err
		}
		req.Filter = f
	}
	switch v.Get("count") {
	case "", "false":
	case "true":
		req.Count = true
	default:
		return Request{}, invalid("count", "count must be true or false.")
	}
	if raw := v.Get("sort"); raw != "" {
		parts := strings.Split(raw, ",")
		if len(parts) > MaxSortKeys {
			return Request{}, tooComplex("sort", "Too many sort keys.")
		}
		for _, p := range parts {
			seg := strings.Split(p, ":")
			if len(seg) < 2 || len(seg) > 3 {
				return Request{}, invalid("sort", "A sort key is field:asc or field:desc, optionally :first or :last.")
			}
			s := SortSpec{Field: seg[0], Dir: seg[1]}
			if len(seg) == 3 {
				s.Nulls = seg[2]
			}
			req.Sort = append(req.Sort, s)
		}
	}
	return req, nil
}

// WriteError writes err as the API error envelope when it is a query error
// and reports whether it did.
func WriteError(w http.ResponseWriter, err error) bool {
	var qe *Error
	if !errors.As(err, &qe) {
		return false
	}
	msg := qe.Message
	if qe.Path != "" {
		msg += " (at " + qe.Path + ")"
	}
	httpx.WriteError(w, qe.status, qe.Code, msg)
	return true
}

// Envelope is the list response of the query endpoints.
type Envelope[T any] struct {
	Items       []T       `json:"items"`
	NextCursor  string    `json:"nextCursor,omitempty"`
	Count       *int      `json:"count,omitempty"`
	CountCapped bool      `json:"countCapped,omitempty"`
	Warnings    []Warning `json:"warnings"`
}

// NewEnvelope maps a Page of domain rows to the response with mapped items.
func NewEnvelope[S, T any](page Page[S], mapItem func(S) T) Envelope[T] {
	out := Envelope[T]{Items: make([]T, 0, len(page.Items)), NextCursor: page.NextCursor, Count: page.Count,
		CountCapped: page.CountCapped, Warnings: page.Warnings}
	if out.Warnings == nil {
		out.Warnings = []Warning{}
	}
	for _, it := range page.Items {
		out.Items = append(out.Items, mapItem(it))
	}
	return out
}
