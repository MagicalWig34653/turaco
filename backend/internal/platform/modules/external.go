package modules

import (
	"fmt"
	"regexp"
	"strings"
)

// HTTP allow-list of external accounts (ADR-0034 point 4). Patterns are declared per module (Module.ExternalPaths)
// and matched segment by segment; there is no regular expression over the whole path and no prefix match unless the
// pattern ends in /*.

type externalPattern struct {
	method   string
	segments []string
	rest     bool // trailing /*
}

var uuidSegment = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func parseExternalPattern(s string) (externalPattern, error) {
	method, path, ok := strings.Cut(s, " ")
	if !ok || method == "" || !strings.HasPrefix(path, apiPrefix) {
		return externalPattern{}, fmt.Errorf("external pattern %q must look like \"METHOD /api/v1/path\"", s)
	}
	if method != strings.ToUpper(method) {
		return externalPattern{}, fmt.Errorf("external pattern %q: the method is upper case", s)
	}
	p := externalPattern{method: method}
	segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if segs[len(segs)-1] == "*" {
		p.rest = true
		segs = segs[:len(segs)-1]
	}
	for _, seg := range segs {
		if seg == "" || strings.Contains(seg, "*") {
			return externalPattern{}, fmt.Errorf("external pattern %q has an empty or wildcard segment", s)
		}
	}
	p.segments = segs
	return p, nil
}

func (p externalPattern) matches(method string, segs []string) bool {
	if method != p.method {
		return false
	}
	if p.rest {
		if len(segs) < len(p.segments) {
			return false
		}
	} else if len(segs) != len(p.segments) {
		return false
	}
	for i, want := range p.segments {
		got := segs[i]
		switch {
		case want == "{id}":
			if !uuidSegment.MatchString(got) {
				return false
			}
		case strings.HasPrefix(want, "{") && strings.HasSuffix(want, "}"):
			// any non-empty segment
		case want != got:
			return false
		}
	}
	return true
}

// ExternalAllowed reports whether an external account may call the route. Only /api/v1 paths are ever allowed; the
// path is matched as received (a trailing slash does not match).
func (ix *Index) ExternalAllowed(method, path string) bool {
	if !strings.HasPrefix(path, apiPrefix) || strings.HasSuffix(path, "/") {
		return false
	}
	segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for _, p := range ix.external {
		if p.matches(method, segs) {
			return true
		}
	}
	return false
}
