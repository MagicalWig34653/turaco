package transport

import "testing"

func TestCSVCellNeutralizesFormulas(t *testing.T) {
	tests := map[string]string{
		"plain":                  "plain",
		"":                       "",
		"=HYPERLINK(\"x\")":      "'=HYPERLINK(\"x\")",
		"+1+1":                   "'+1+1",
		"-2":                     "'-2",
		"@SUM(A1)":               "'@SUM(A1)",
		"\t=1":                   "'\t=1",
		"  =cmd|' /C calc'!A0":   "'  =cmd|' /C calc'!A0",
		"a=b":                    "a=b",
		"0x87D1041C":             "0x87D1041C",
		"line\nbreak":            "line break",
		"\r=1":                   "' =1",
		"PC-01 (=not a formula)": "PC-01 (=not a formula)",
	}
	for in, want := range tests {
		if got := csvCell(in); got != want {
			t.Errorf("csvCell(%q) = %q, want %q", in, got, want)
		}
	}
}
