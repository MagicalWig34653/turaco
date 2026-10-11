package search

import (
	"slices"
	"testing"
)

func TestSubstringPatterns(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"Etikett", []string{"%etikett%"}},
		{"Gäste-WLAN", []string{"%gaste%", "%wlan%"}},
		{"Straße/Büro", []string{"%strasse%", "%buro%"}},
		{"VPN am Büro", []string{"%vpn%", "%buro%"}},
		{"ab", []string{"%ab%"}},
		{"ab cd", []string{"%ab%", "%cd%"}},
		{"100%_x", []string{"%100\\%\\_x%"}},
		{`a\b`, []string{`%a\\b%`}},
		{"a bb ccc d", []string{"%ccc%"}},
		{"aaa bbb ccc ddd eee fff ggg hhh iii", []string{"%aaa%", "%bbb%", "%ccc%", "%ddd%", "%eee%", "%fff%", "%ggg%", "%hhh%"}},
	}
	for _, c := range cases {
		if got := SubstringPatterns(c.in); !slices.Equal(got, c.want) {
			t.Errorf("SubstringPatterns(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
