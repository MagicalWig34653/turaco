package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSecretFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for name, c := range map[string]struct{ content, want string }{
		"plain": {"s3cret", "s3cret"}, "lf": {"s3cret\n", "s3cret"}, "crlf": {"s3cret\r\n", "s3cret"},
		"inner space kept": {" a b \n", " a b "}, "only one newline removed": {"s\n\n", "s\n"},
	} {
		got, err := ReadSecretFile(write(name, c.content))
		if err != nil || got != c.want {
			t.Errorf("%s: %q %v, want %q", name, got, err, c.want)
		}
	}
	for name, p := range map[string]string{
		"empty": write("empty", ""), "only newline": write("nl", "\n"),
		"too large": write("big", strings.Repeat("x", maxSecretFileSize+1)), "missing": filepath.Join(dir, "nope"),
	} {
		if _, err := ReadSecretFile(p); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}
