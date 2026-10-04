package application

import (
	"slices"
	"strconv"
	"strings"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/advisories"
)

// Version is a parsed software version: dotted numbers (1 to 10 parts of at most 9 digits) with an
// optional pre-release suffix after "-" (dot-separated alphanumeric identifiers). A leading "v" and build
// metadata after "+" are ignored. Anything else (for example "1.2a" or "2024 R2") is not comparable.
type Version struct {
	nums []uint64
	pre  []string
}

// ParseVersion parses a version; ok is false when it is not comparable.
func ParseVersion(s string) (v Version, ok bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "v"), "V")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		if !identifiers(s[i+1:]) {
			return Version{}, false
		}
		s = s[:i]
	}
	core := s
	if i := strings.IndexByte(s, '-'); i >= 0 {
		core = s[:i]
		pre := s[i+1:]
		if !identifiers(pre) {
			return Version{}, false
		}
		v.pre = strings.Split(pre, ".")
	}
	parts := strings.Split(core, ".")
	if len(parts) == 0 || len(parts) > 10 {
		return Version{}, false
	}
	for _, p := range parts {
		if p == "" || len(p) > 9 || strings.Trim(p, "0123456789") != "" {
			return Version{}, false
		}
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return Version{}, false
		}
		v.nums = append(v.nums, n)
	}
	return v, true
}

// identifiers reports dot-separated, non-empty alphanumeric identifiers.
func identifiers(s string) bool {
	if s == "" || len(s) > 60 {
		return false
	}
	for _, id := range strings.Split(s, ".") {
		if id == "" {
			return false
		}
		for _, r := range id {
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
				return false
			}
		}
	}
	return true
}

func numeric(s string) bool { return strings.Trim(s, "0123456789") == "" }

// Compare returns -1, 0 or 1. Missing numeric parts count as zero (1.2 equals 1.2.0); a pre-release is
// lower than the release; pre-release identifiers compare numerically when both are numbers, numbers
// are lower than words and words compare by ASCII.
func (v Version) Compare(o Version) int {
	for i := 0; i < max(len(v.nums), len(o.nums)); i++ {
		var a, b uint64
		if i < len(v.nums) {
			a = v.nums[i]
		}
		if i < len(o.nums) {
			b = o.nums[i]
		}
		if a != b {
			if a < b {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(v.pre) == 0 && len(o.pre) == 0:
		return 0
	case len(v.pre) == 0:
		return 1
	case len(o.pre) == 0:
		return -1
	}
	for i := 0; i < min(len(v.pre), len(o.pre)); i++ {
		a, b := v.pre[i], o.pre[i]
		if a == b {
			continue
		}
		an, bn := numeric(a), numeric(b)
		switch {
		case an && bn:
			x, y := strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
			if len(x) < len(y) || len(x) == len(y) && x < y {
				return -1
			}
			if len(x) > len(y) || x > y {
				return 1
			}
			continue
		case an:
			return -1
		case bn:
			return 1
		case a < b:
			return -1
		default:
			return 1
		}
	}
	switch {
	case len(v.pre) < len(o.pre):
		return -1
	case len(v.pre) > len(o.pre):
		return 1
	}
	return 0
}

// Rule is one version condition of a Criterion (kinds: advisories.RuleKinds).
type Rule struct {
	Kind    string
	Version string
}

// Affected evaluates the version rules against an installed version. comparable is false when the
// installed version or a rule version cannot be compared; affected is then meaningless. No rules means
// every version is affected. Otherwise a version is affected when it equals an eq rule, lies below an lt
// rule or at/below an le rule, or lies in an introduced/fixed range: the events are sorted by version and
// the version is affected when the last event at or below it is an introduced one (fixed is exclusive; a
// fixed without any introduced starts the range at the lowest version).
func Affected(rules []Rule, installed string) (affected, comparable bool) {
	if len(rules) == 0 {
		return true, true
	}
	v, ok := ParseVersion(installed)
	if !ok {
		return false, false
	}
	type event struct {
		v          Version
		introduced bool
	}
	var events []event
	hasIntroduced := false
	for _, r := range rules {
		rv, ok := ParseVersion(r.Version)
		if !ok {
			return false, false
		}
		c := v.Compare(rv)
		switch r.Kind {
		case advisories.RuleEQ:
			affected = affected || c == 0
		case advisories.RuleLT:
			affected = affected || c < 0
		case advisories.RuleLE:
			affected = affected || c <= 0
		case advisories.RuleIntroduced:
			hasIntroduced = true
			events = append(events, event{v: rv, introduced: true})
		case advisories.RuleFixed:
			events = append(events, event{v: rv})
		default:
			return false, false
		}
	}
	if len(events) > 0 {
		// Ties: fixed sorts before introduced, so "introduced X" after "fixed X" re-opens the range.
		slices.SortStableFunc(events, func(a, b event) int {
			if c := a.v.Compare(b.v); c != 0 {
				return c
			}
			switch {
			case a.introduced == b.introduced:
				return 0
			case a.introduced:
				return 1
			default:
				return -1
			}
		})
		in := !hasIntroduced
		for _, e := range events {
			if e.v.Compare(v) > 0 {
				break
			}
			in = e.introduced
		}
		affected = affected || in
	}
	return affected, true
}
