package query

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

// sortKey is one resolved ORDER BY key.
type sortKey struct {
	expr       string
	cast       string
	desc       bool
	nullable   bool
	nullsFirst bool
}

func (k sortKey) orderSQL() string {
	s := k.expr
	if k.desc {
		s += " DESC"
	} else {
		s += " ASC"
	}
	if k.nullable {
		if k.nullsFirst {
			s += " NULLS FIRST"
		} else {
			s += " NULLS LAST"
		}
	}
	return s
}

var sortCasts = map[Type]string{TypeText: "text", TypeEnum: "text", TypeNumber: "numeric", TypeBoolean: "boolean",
	TypeDate: "date", TypeDateTime: "timestamptz", TypeReference: "uuid"}

func errUnindexedSort(path string) *Error {
	return &Error{Code: CodeUnindexedSort, Message: "The field cannot be sorted.", Path: path, status: http.StatusBadRequest}
}

// resolveSort validates the requested sort (or takes the default) and returns
// the keys, the normalised specs (for the cursor hash) and the implicit
// unique tiebreaker as the last key.
func (c *Catalog) resolveSort(subj Subject, specs []SortSpec) ([]sortKey, []SortSpec, error) {
	if len(specs) == 0 {
		specs = c.res.DefaultSort
	}
	if len(specs) > MaxSortKeys {
		return nil, nil, tooComplex("sort", "Too many sort keys.")
	}
	var keys []sortKey
	var norm []SortSpec
	seen := map[string]bool{}
	for i, s := range specs {
		path := "sort[" + itoa(i) + "]"
		f, ok := c.get(subj, s.Field)
		if !ok || !f.Sortable {
			return nil, nil, invalid(path, "Unknown or unavailable field.")
		}
		if seen[f.Key] {
			return nil, nil, invalid(path, "A field can be sorted only once.")
		}
		seen[f.Key] = true
		if !f.SortIndexed {
			return nil, nil, errUnindexedSort(path)
		}
		if s.Dir != "asc" && s.Dir != "desc" {
			return nil, nil, invalid(path, "The direction must be asc or desc.")
		}
		if s.Nulls != "" && s.Nulls != "first" && s.Nulls != "last" {
			return nil, nil, invalid(path, "Nulls must be first or last.")
		}
		desc := s.Dir == "desc"
		nullsFirst := desc // PostgreSQL default: nulls are the largest value
		if s.Nulls != "" {
			nullsFirst = s.Nulls == "first"
		}
		expr, cast := f.Column.sql, sortCasts[f.Type]
		switch {
		case f.SortByEnumOrder:
			expr, cast = Ordinal(f.Column, f.enumOrder()...).sql, "integer"
		case !f.SortColumn.zero():
			expr = f.SortColumn.sql
		}
		keys = append(keys, sortKey{expr: expr, cast: cast, desc: desc, nullable: f.Nullable, nullsFirst: nullsFirst})
		n := SortSpec{Field: f.Key, Dir: s.Dir}
		if f.Nullable {
			n.Nulls = "last"
			if nullsFirst {
				n.Nulls = "first"
			}
		}
		norm = append(norm, n)
	}
	last := keys[len(keys)-1]
	keys = append(keys, sortKey{expr: c.res.Alias + "." + c.res.IDColumn, cast: "uuid", desc: last.desc})
	return keys, norm, nil
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func castParam(arg string, cast string) string {
	if cast == "text" {
		return arg + "::text"
	}
	return arg + "::text::" + cast
}

// keysetPredicate builds the "rows after the cursor" condition
// lexicographically per key (never a row comparison, because keys mix
// directions and NULL placement). For keys k1..kn it is the OR over i of
// (k1 = c1 AND .. AND k(i-1) = c(i-1) AND after(ki, ci)); equality uses
// IS NULL for a NULL cursor value, and after(k, c) has an explicit NULL
// branch per direction and placement.
func keysetPredicate(keys []sortKey, vals []*string, arg func(any) string) string {
	var terms []string
	for i, k := range keys {
		v := vals[i]
		if v == nil && !k.nullsFirst {
			continue // nothing follows NULLs placed last within this key
		}
		// Placeholders are numbered in text order, so the arguments are
		// collected in the same order: equalities first, then the "after" part.
		parts := make([]string, 0, i+1)
		for j := 0; j < i; j++ {
			if vals[j] == nil {
				parts = append(parts, keys[j].expr+" IS NULL")
			} else {
				parts = append(parts, keys[j].expr+" = "+castParam(arg(*vals[j]), keys[j].cast))
			}
		}
		if v == nil {
			parts = append(parts, k.expr+" IS NOT NULL") // only non-null values follow NULLs placed first
		} else {
			sym := ">"
			if k.desc {
				sym = "<"
			}
			after := k.expr + " " + sym + " " + castParam(arg(*v), k.cast)
			if k.nullable && !k.nullsFirst {
				after = "(" + after + " OR " + k.expr + " IS NULL)"
			}
			parts = append(parts, after)
		}
		terms = append(terms, "("+strings.Join(parts, " AND ")+")")
	}
	if len(terms) == 0 {
		return "FALSE"
	}
	return "(" + strings.Join(terms, " OR ") + ")"
}

// ---- signed cursor ----

// CursorCodec signs and verifies opaque keyset cursors.
type CursorCodec struct{ key []byte }

// NewCursorCodec derives the signing key from secret.
func NewCursorCodec(secret []byte) *CursorCodec {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("turaco/query/cursor/v1"))
	return &CursorCodec{key: m.Sum(nil)}
}

type cursorPayload struct {
	H string    `json:"h"`
	K []*string `json:"k"`
}

const maxCursorLen = 4096

func (cc *CursorCodec) sign(payload []byte) []byte {
	m := hmac.New(sha256.New, cc.key)
	m.Write(payload)
	return m.Sum(nil)
}

func (cc *CursorCodec) encode(hash string, vals []*string) string {
	raw, _ := json.Marshal(cursorPayload{H: hash, K: vals})
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(cc.sign(raw))
}

// decode verifies signature, request hash and shape.
func (cc *CursorCodec) decode(s, hash string, keys []sortKey) ([]*string, error) {
	if len(s) > maxCursorLen {
		return nil, ErrInvalidCursor
	}
	body, sig, ok := strings.Cut(s, ".")
	if !ok {
		return nil, ErrInvalidCursor
	}
	raw, err1 := base64.RawURLEncoding.DecodeString(body)
	mac, err2 := base64.RawURLEncoding.DecodeString(sig)
	if err1 != nil || err2 != nil || !hmac.Equal(mac, cc.sign(raw)) {
		return nil, ErrInvalidCursor
	}
	var p cursorPayload
	if err := json.Unmarshal(raw, &p); err != nil || p.H != hash || len(p.K) != len(keys) {
		return nil, ErrInvalidCursor
	}
	if id := p.K[len(p.K)-1]; id == nil || !isUUID(*id) {
		return nil, ErrInvalidCursor
	}
	return p.K, nil
}

// requestHash binds a cursor to the resource, the caller, the scope and the
// effective filter and sort, so a cursor cannot be replayed with another
// query or by another user.
func requestHash(resource, scope, userID string, f *Filter, sort []SortSpec) string {
	h := sha256.New()
	enc := json.NewEncoder(h)
	root := ""
	if f != nil && f.Root != nil {
		if b, err := json.Marshal(f.Root); err == nil {
			root = string(b)
		}
	}
	search := ""
	if f != nil {
		search = strings.TrimSpace(f.Search)
	}
	_ = enc.Encode([]any{resource, scope, userID, root, search, sort})
	return hex.EncodeToString(h.Sum(nil))[:32]
}
