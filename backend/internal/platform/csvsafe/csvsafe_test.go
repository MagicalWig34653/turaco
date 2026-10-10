package csvsafe

import (
	"bytes"
	"strings"
	"testing"
)

func TestCellNeutralizesFormulas(t *testing.T) {
	for in, want := range map[string]string{
		"=1+1":          "'=1+1",
		"  =cmd|' /C'":  "'  =cmd|' /C'",
		"+49 170":       "'+49 170",
		"-5":            "'-5",
		"@SUM(A1)":      "'@SUM(A1)",
		"\tTAB":         "'\tTAB",
		"plain":         "plain",
		"":              "",
		"a=b":           "a=b",
		"line\r\nbreak": "linebreak",
		"\x00=evil":     "'=evil",
		"  ":            "  ",
		"Müller, Anna":  "Müller, Anna",
		" =nbsp":        " =nbsp",
	} {
		if got := Cell(in); got != want {
			t.Errorf("Cell(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriterBOMAndQuoting(t *testing.T) {
	var b bytes.Buffer
	w := NewWriter(&b)
	if err := w.Write([]string{"a", "=x", "with,comma", "q\"uote"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Write([]string{"b", "c"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if !strings.HasPrefix(out, "\ufeff") || strings.Count(out, "\ufeff") != 1 {
		t.Fatalf("BOM: %q", out)
	}
	if !strings.Contains(out, `a,'=x,"with,comma","q""uote"`) {
		t.Fatalf("row: %q", out)
	}
}
