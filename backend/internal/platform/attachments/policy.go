package attachments

import (
	"bytes"
	"mime"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// HeadSize is how many leading bytes are used to confirm the content type.
const HeadSize = 512

type typeDef struct {
	// match confirms the leading bytes belong to the type.
	match func(head []byte) bool
	// inDefault marks the types allowed unless ATTACHMENT_ALLOWED_TYPES overrides the list.
	inDefault bool
}

const (
	typeDocx = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	typeXlsx = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	typePptx = "application/vnd.openxmlformats-officedocument.presentationml.presentation"
)

var knownTypes = map[string]typeDef{
	"application/pdf": {func(h []byte) bool { return bytes.HasPrefix(h, []byte("%PDF-")) }, true},
	"image/png":       {func(h []byte) bool { return bytes.HasPrefix(h, []byte("\x89PNG\r\n\x1a\n")) }, true},
	"image/jpeg":      {func(h []byte) bool { return bytes.HasPrefix(h, []byte{0xFF, 0xD8, 0xFF}) }, true},
	"image/gif": {func(h []byte) bool {
		return bytes.HasPrefix(h, []byte("GIF87a")) || bytes.HasPrefix(h, []byte("GIF89a"))
	}, true},
	"image/webp": {func(h []byte) bool {
		return len(h) >= 12 && bytes.HasPrefix(h, []byte("RIFF")) && bytes.Equal(h[8:12], []byte("WEBP"))
	}, true},
	"text/plain": {plainText, true},
	"text/csv":   {plainText, true},
	typeDocx:     {zipMagic, true},
	typeXlsx:     {zipMagic, true},
	typePptx:     {zipMagic, true},
	// Archives are opt-in: they hide content from the scanner's type heuristics and from reviewers.
	"application/zip": {zipMagic, false},
}

func zipMagic(h []byte) bool { return bytes.HasPrefix(h, []byte("PK\x03\x04")) }

// plainText accepts valid UTF-8 without NUL or control characters other than tab, line breaks and form feed. The
// head may end inside a multi-byte sequence.
func plainText(h []byte) bool {
	for len(h) > 0 {
		r, n := utf8.DecodeRune(h)
		if r == utf8.RuneError && n <= 1 {
			if !utf8.FullRune(h) { // truncated at the end of the head
				return true
			}
			return false
		}
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' && r != '\f' || r == 0x7f {
			return false
		}
		h = h[n:]
	}
	return true
}

// Policy is the content-type allow-list and the size cap.
type Policy struct {
	MaxBytes int64
	allowed  []string
}

// NewPolicy builds the policy from the configured list; an empty list selects the defaults. Unknown types are an
// error so a typo cannot silently widen or shrink the list.
func NewPolicy(maxBytes int64, types []string) (Policy, error) {
	p := Policy{MaxBytes: maxBytes}
	if len(types) == 0 {
		for t, d := range knownTypes {
			if d.inDefault {
				p.allowed = append(p.allowed, t)
			}
		}
	}
	for _, t := range types {
		t = strings.ToLower(strings.TrimSpace(t))
		if _, ok := knownTypes[t]; !ok {
			return Policy{}, &InvalidError{Message: "ATTACHMENT_ALLOWED_TYPES: unsupported content type " + t}
		}
		if !slices.Contains(p.allowed, t) {
			p.allowed = append(p.allowed, t)
		}
	}
	slices.Sort(p.allowed)
	return p, nil
}

// AllowedTypes returns the allowed content types, sorted.
func (p Policy) AllowedTypes() []string { return slices.Clone(p.allowed) }

// Check validates the declared content type against the allow-list and the leading bytes, and returns the
// normalized type to store.
func (p Policy) Check(declared string, head []byte) (string, error) {
	mt, _, err := mime.ParseMediaType(declared)
	if err != nil {
		return "", ErrUnsupportedType
	}
	mt = strings.ToLower(mt)
	if !slices.Contains(p.allowed, mt) {
		return "", ErrUnsupportedType
	}
	if !knownTypes[mt].match(head) {
		return "", ErrUnsupportedType
	}
	return mt, nil
}

// CleanFileName reduces a client file name to a display string: base name only, no control or bidirectional
// override characters, at most 200 runes. It is data, never a path.
func CleanFileName(name string) string {
	if !utf8.ValidString(name) {
		name = strings.ToValidUTF8(name, "")
	}
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	n := 0
	for _, r := range name {
		switch {
		case unicode.IsControl(r), r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0x200E, r == 0x200F, r == 0xFEFF:
			continue
		}
		if n >= 200 {
			break
		}
		b.WriteRune(r)
		n++
	}
	out := strings.Trim(strings.TrimSpace(b.String()), ".")
	if out == "" {
		return "file"
	}
	return out
}
