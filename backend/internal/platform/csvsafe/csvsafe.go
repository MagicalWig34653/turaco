// Package csvsafe writes CSV that is safe to open in a spreadsheet: every cell is neutralized against formula
// injection and control characters, and the output starts with a UTF-8 byte order mark so Excel reads umlauts.
// It is the one CSV writer of the product; modules do not format cells themselves.
package csvsafe

import (
	"encoding/csv"
	"io"
	"strings"
)

// Cell neutralizes one value. Control characters except a tab are dropped; a cell whose first character after
// leading spaces is =, +, -, @ or a tab is prefixed with an apostrophe so a spreadsheet shows it as text.
func Cell(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if t := strings.TrimLeft(s, " "); t != "" {
		switch t[0] {
		case '=', '+', '-', '@', '\t':
			return "'" + s
		}
	}
	return s
}

// Writer writes neutralized rows.
type Writer struct {
	w     *csv.Writer
	out   io.Writer
	begun bool
}

// NewWriter returns a Writer over out. The byte order mark is written with the first row.
func NewWriter(out io.Writer) *Writer { return &Writer{w: csv.NewWriter(out), out: out} }

// Write neutralizes and writes one row.
func (w *Writer) Write(row []string) error {
	if !w.begun {
		w.begun = true
		if _, err := io.WriteString(w.out, "\ufeff"); err != nil {
			return err
		}
	}
	safe := make([]string, len(row))
	for i, c := range row {
		safe[i] = Cell(c)
	}
	return w.w.Write(safe)
}

// Flush writes buffered data and reports a write error, if any.
func (w *Writer) Flush() error {
	w.w.Flush()
	return w.w.Error()
}
