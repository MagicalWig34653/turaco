package ldap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSecret(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bind-password")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadPasswordFile(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"no newline", "hunter2", "hunter2"},
		{"trailing LF", "hunter2\n", "hunter2"},
		{"trailing CRLF", "hunter2\r\n", "hunter2"},
		{"only one LF removed", "hunter2\n\n", "hunter2\n"},
		{"only one CRLF removed", "hunter2\r\n\r\n", "hunter2\r\n"},
		{"lone CR kept", "hunter2\r", "hunter2\r"},
		{"inner and edge spaces kept", "  pass word  \n", "  pass word  "},
		{"unicode", "pässwörd\n", "pässwörd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ReadPasswordFile(writeSecret(t, tt.content))
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReadPasswordFileRejectsEmpty(t *testing.T) {
	for _, content := range []string{"", "\n", "\r\n"} {
		_, err := ReadPasswordFile(writeSecret(t, content))
		if err == nil || !strings.Contains(err.Error(), "empty") {
			t.Errorf("content %q: err = %v, want empty-password error", content, err)
		}
	}
}

func TestReadPasswordFileMissingFile(t *testing.T) {
	_, err := ReadPasswordFile(filepath.Join(t.TempDir(), "missing"))
	if err == nil || !strings.Contains(err.Error(), "ldap: read bind password file") {
		t.Fatalf("err = %v", err)
	}
}

func TestReadPasswordFileErrorsNeverContainContent(t *testing.T) {
	// A directory cannot be read as a file; an oversized file is rejected.
	// Neither error may echo file content.
	secret := strings.Repeat("TopSecret", maxPasswordFileSize/4)
	_, err := ReadPasswordFile(writeSecret(t, secret))
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v, want too-large error", err)
	}
	if strings.Contains(err.Error(), "TopSecret") {
		t.Errorf("error leaks file content: %v", err)
	}
	if _, err := ReadPasswordFile(t.TempDir()); err == nil {
		t.Error("reading a directory must fail")
	}
}
