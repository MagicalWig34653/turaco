package attachments

import (
	"errors"
	"strings"
	"testing"
)

func TestPolicyCheck(t *testing.T) {
	p, err := NewPolicy(1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	png := []byte("\x89PNG\r\n\x1a\n....")
	cases := []struct {
		name, declared string
		head           []byte
		ok             bool
	}{
		{"pdf", "application/pdf", []byte("%PDF-1.7\n"), true},
		{"pdf with parameters and case", "Application/PDF; charset=binary", []byte("%PDF-1.4"), true},
		{"png", "image/png", png, true},
		{"png declared as pdf", "application/pdf", png, false},
		{"html declared as text", "text/html", []byte("<html>"), false},
		{"svg is not allowed", "image/svg+xml", []byte("<svg>"), false},
		{"executable declared as png", "image/png", []byte("MZ\x90\x00"), false},
		{"script declared as text", "text/plain", []byte("echo hi\x00\x01"), false},
		{"plain text", "text/plain", []byte("Hello\nWörld\t!"), true},
		{"text truncated mid rune", "text/plain", []byte("abc\xc3"), true},
		{"invalid utf-8 text", "text/plain", []byte("abc\xff\xfe"), false},
		{"docx", typeDocx, []byte("PK\x03\x04rest"), true},
		{"docx without zip magic", typeDocx, []byte("%PDF-"), false},
		{"zip is opt-in", "application/zip", []byte("PK\x03\x04"), false},
		{"octet-stream", "application/octet-stream", []byte("anything"), false},
		{"empty declared", "", []byte("%PDF-"), false},
		{"garbage declared", "///", []byte("%PDF-"), false},
		{"webp", "image/webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), true},
		{"jpeg", "image/jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0}, true},
	}
	for _, c := range cases {
		got, err := p.Check(c.declared, c.head)
		if (err == nil) != c.ok {
			t.Errorf("%s: err=%v", c.name, err)
		}
		if err != nil && !errors.Is(err, ErrUnsupportedType) {
			t.Errorf("%s: wrong error %v", c.name, err)
		}
		if c.ok && strings.Contains(got, ";") {
			t.Errorf("%s: parameters kept: %s", c.name, got)
		}
	}
}

func TestNewPolicyOverride(t *testing.T) {
	p, err := NewPolicy(1024, []string{"application/PDF", "application/zip", "application/pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(p.AllowedTypes(), ","); got != "application/pdf,application/zip" {
		t.Fatalf("got %s", got)
	}
	if _, err := p.Check("image/png", []byte("\x89PNG\r\n\x1a\n")); err == nil {
		t.Fatal("override must replace the defaults")
	}
	if _, err := NewPolicy(1024, []string{"image/png", "text/html"}); err == nil {
		t.Fatal("unknown type accepted")
	}
}

func TestCleanFileName(t *testing.T) {
	cases := map[string]string{
		"report.pdf":                      "report.pdf",
		"../../etc/passwd":                "passwd",
		`C:\Users\x\evil.exe`:             "evil.exe",
		"a\x00b\r\n.txt":                  "ab.txt",
		"fdp.\u202Eexe":                   "fdp.exe",
		"":                                "file",
		"...":                             "file",
		"  spaced name .pdf ":             "spaced name .pdf",
		strings.Repeat("ä", 300) + ".pdf": strings.Repeat("ä", 200),
		"bad\xffutf8.txt":                 "badutf8.txt",
	}
	for in, want := range cases {
		if got := CleanFileName(in); got != want {
			t.Errorf("CleanFileName(%q) = %q, want %q", in, got, want)
		}
	}
}
