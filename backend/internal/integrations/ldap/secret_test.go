package ldap

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func assertNoPassword(t *testing.T, what, out string) {
	t.Helper()
	if strings.Contains(out, testBindPassword) {
		t.Errorf("%s leaks the bind password: %q", what, out)
	}
}

func TestSourceNeverFormatsBindPassword(t *testing.T) {
	src, err := NewSource(adConfig(), testBindPassword, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d"} {
		assertNoPassword(t, "Sprintf("+verb+", *Source)", fmt.Sprintf(verb, src))
		assertNoPassword(t, "Sprintf("+verb+", Source)", fmt.Sprintf(verb, *src))
		assertNoPassword(t, "Sprintf("+verb+", secret)", fmt.Sprintf(verb, src.password))
	}
	if got := fmt.Sprintf("%+v", src.password); got != "[REDACTED]" {
		t.Errorf("secret formatted as %q", got)
	}
	if got := fmt.Sprint(src.password); got != "[REDACTED]" {
		t.Errorf("secret printed as %q", got)
	}
	if src.password.reveal() != testBindPassword {
		t.Error("reveal does not return the password")
	}
	if (secret{}).reveal() != "" {
		t.Error("zero secret reveals something")
	}
}

func TestSourceNeverLogsBindPassword(t *testing.T) {
	src, err := NewSource(adConfig(), testBindPassword, nil)
	if err != nil {
		t.Fatal(err)
	}
	type holder struct {
		Password secret
		Source   *Source
	}
	for name, newHandler := range map[string]func(*bytes.Buffer) slog.Handler{
		"json": func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
		"text": func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
	} {
		var buf bytes.Buffer
		logger := slog.New(newHandler(&buf))
		logger.Info("test", "source", src, "sourceValue", *src, "password", src.password, "holder", holder{src.password, src})
		logger.Info("test", slog.Any("password", src.password), slog.Group("g", slog.Any("p", src.password)))
		assertNoPassword(t, name+" slog output", buf.String())
		if !strings.Contains(buf.String(), "[REDACTED]") {
			t.Errorf("%s slog output has no redaction marker: %q", name, buf.String())
		}
	}
}

func TestSecretMarshalsRedacted(t *testing.T) {
	b, err := json.Marshal(struct{ Password secret }{newSecret(testBindPassword)})
	if err != nil {
		t.Fatal(err)
	}
	assertNoPassword(t, "json", string(b))
	if string(b) != `{"Password":"[REDACTED]"}` {
		t.Errorf("json = %s", b)
	}
}
