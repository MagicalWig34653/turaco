// Package safetext rejects characters that make user-supplied text misleading
// when it is shown elsewhere (notification emails, lists): control characters
// and invisible or bidirectional-override characters. Names, titles and
// reasons that end up in other people's inboxes must not be able to hide or
// reorder text.
package safetext

import "unicode"

// Unsafe reports whether r must not appear in a single-line text field.
func Unsafe(r rune) bool {
	if unicode.IsControl(r) {
		return true
	}
	switch {
	case r == 0x061C, // Arabic letter mark
		r == 0x200B,              // zero width space
		r == 0x200E, r == 0x200F, // left/right-to-right marks
		r >= 0x202A && r <= 0x202E, // embeddings and overrides
		r == 0x2060,                // word joiner
		r >= 0x2066 && r <= 0x2069, // isolates
		r == 0xFEFF:                // byte order mark / zero width no-break space
		return true
	}
	return false
}

// ContainsUnsafe reports whether s contains an unsafe rune. With multiline,
// line feed, carriage return and tab are allowed (descriptions).
func ContainsUnsafe(s string, multiline bool) bool {
	for _, r := range s {
		if multiline && (r == '\n' || r == '\r' || r == '\t') {
			continue
		}
		if Unsafe(r) {
			return true
		}
	}
	return false
}
