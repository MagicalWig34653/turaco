package application

import "testing"

func TestParseAndCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0},
		{"1.2", "1.2.0", 0},
		{"v1.2.3", "1.2.3", 0},
		{"1.2.3+build.5", "1.2.3", 0},
		{"1.10", "1.9", 1},
		{"10.0.19045.3803", "10.0.19045.4046", -1},
		{"1.2.3-beta", "1.2.3", -1},
		{"1.2.3-alpha", "1.2.3-beta", -1},
		{"1.2.3-rc.2", "1.2.3-rc.10", -1},
		{"1.2.3-1", "1.2.3-alpha", -1},
		{"1.2.3-rc", "1.2.3-rc.1", -1},
		{"2", "1.99.99", 1},
	}
	for _, c := range cases {
		a, okA := ParseVersion(c.a)
		b, okB := ParseVersion(c.b)
		if !okA || !okB {
			t.Fatalf("%q/%q must parse", c.a, c.b)
		}
		if got := a.Compare(b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
		if got := b.Compare(a); got != -c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d (antisymmetry)", c.b, c.a, got, -c.want)
		}
	}
	for _, bad := range []string{"", "1.2a", "2024 R2", "1..2", "1.2.", "-beta", "1.2.3-", "1.2.3-be ta", "1.2.3+", "1234567890.1", "1.2.3.4.5.6.7.8.9.10.11"} {
		if _, ok := ParseVersion(bad); ok {
			t.Errorf("%q must not be comparable", bad)
		}
	}
}

func TestAffected(t *testing.T) {
	r := func(kind, v string) Rule { return Rule{Kind: kind, Version: v} }
	cases := []struct {
		name       string
		rules      []Rule
		installed  string
		affected   bool
		comparable bool
	}{
		{"no rules: every version", nil, "anything goes", true, true},
		{"below fixed", []Rule{r("fixed", "2.0.1")}, "2.0.0", true, true},
		{"fixed boundary is not affected", []Rule{r("fixed", "2.0.1")}, "2.0.1", false, true},
		{"above fixed", []Rule{r("fixed", "2.0.1")}, "3.0", false, true},
		{"before introduced", []Rule{r("introduced", "1.5"), r("fixed", "2.0")}, "1.4.9", false, true},
		{"at introduced", []Rule{r("introduced", "1.5"), r("fixed", "2.0")}, "1.5", true, true},
		{"pre-release of fixed is affected", []Rule{r("fixed", "2.0")}, "2.0-rc1", true, true},
		{"second range", []Rule{r("introduced", "1.0"), r("fixed", "1.2"), r("introduced", "2.0"), r("fixed", "2.3")}, "2.1", true, true},
		{"gap between ranges", []Rule{r("introduced", "1.0"), r("fixed", "1.2"), r("introduced", "2.0"), r("fixed", "2.3")}, "1.5", false, true},
		{"introduced without fixed", []Rule{r("introduced", "3.0")}, "9.0", true, true},
		{"lt", []Rule{r("lt", "5.0")}, "4.9", true, true},
		{"lt boundary", []Rule{r("lt", "5.0")}, "5.0", false, true},
		{"le boundary", []Rule{r("le", "5.0")}, "5.0", true, true},
		{"eq", []Rule{r("eq", "1.2.3"), r("eq", "1.2.5")}, "1.2.5", true, true},
		{"eq miss", []Rule{r("eq", "1.2.3")}, "1.2.4", false, true},
		{"incomparable installed", []Rule{r("fixed", "2.0")}, "2.0 build 7", false, false},
		{"empty installed", []Rule{r("fixed", "2.0")}, "", false, false},
		{"incomparable rule", []Rule{r("fixed", "2.0-final!")}, "1.0", false, false},
		{"unknown kind", []Rule{r("gt", "1.0")}, "2.0", false, false},
	}
	for _, c := range cases {
		a, cmp := Affected(c.rules, c.installed)
		if cmp != c.comparable || (cmp && a != c.affected) {
			t.Errorf("%s: Affected = %v,%v want %v,%v", c.name, a, cmp, c.affected, c.comparable)
		}
	}
}
