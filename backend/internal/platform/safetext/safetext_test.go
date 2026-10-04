package safetext

import "testing"

func TestContainsUnsafe(t *testing.T) {
	for name, s := range map[string]string{
		"nul": "a\x00b", "newline": "a\nb", "tab": "a\tb", "bell": "a\x07b", "rtl override": "a‮b",
		"isolate": "a⁦b", "zero width space": "a​b", "bom": "\ufeffa", "left-to-right mark": "a‎b",
		"word joiner": "a⁠b", "arabic letter mark": "a؜b",
		"soft hyphen": "a\u00adb", "line separator": "a\u2028b", "paragraph separator": "a\u2029b",
		"narrow no-break space": "a\u202fb",
	} {
		if !ContainsUnsafe(s, false) {
			t.Errorf("%s: single-line text must be rejected", name)
		}
	}
	for name, s := range map[string]string{
		"plain": "Replace toner", "german": "Größe ändern", "emoji": "Done ✅", "zwj emoji": "👩‍💻",
		"cjk": "サーバー再起動", "rtl text without controls": "שלום",
	} {
		if ContainsUnsafe(s, false) {
			t.Errorf("%s: must be accepted", name)
		}
	}
	if ContainsUnsafe("line1\nline2\r\n\tindent", true) != false {
		t.Error("multiline text allows line breaks and tabs")
	}
	if !ContainsUnsafe("a‮b\nc", true) || !ContainsUnsafe("a\x00b", true) ||
		!ContainsUnsafe("a\u2028b", true) || !ContainsUnsafe("a\u2029b", true) {
		t.Error("multiline text still rejects overrides and other control characters")
	}
}
