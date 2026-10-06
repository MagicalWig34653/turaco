package nvd

import (
	"regexp"
	"strings"
)

// cpe is the part of a CPE 2.3 formatted string the mapping uses.
type cpe struct {
	part, vendor, product, version, targetSW string
}

// parseCPE23 splits "cpe:2.3:<part>:<vendor>:<product>:<version>:<update>:<edition>:<language>:<sw_edition>:
// <target_sw>:<target_hw>:<other>"; a colon inside a component is escaped as "\:". The components keep
// their wildcard semantics: "*" (any) and "-" (not applicable) are returned as given.
func parseCPE23(s string) (cpe, bool) {
	fields := splitEscaped(s)
	if len(fields) != 13 || fields[0] != "cpe" || fields[1] != "2.3" {
		return cpe{}, false
	}
	c := cpe{part: fields[2], vendor: unescapeCPE(fields[3]), product: unescapeCPE(fields[4]),
		version: unescapeCPE(fields[5]), targetSW: unescapeCPE(fields[10])}
	if c.part == "" || c.vendor == "" || c.product == "" {
		return cpe{}, false
	}
	return c, true
}

func splitEscaped(s string) []string {
	var out []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\' && i+1 < len(s):
			cur.WriteByte(s[i])
			cur.WriteByte(s[i+1])
			i++
		case s[i] == ':':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(s[i])
		}
	}
	return append(out, cur.String())
}

// unescapeCPE removes the backslash quoting of CPE 2.3 components.
func unescapeCPE(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

var versionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+~-]{0,99}$`)

// wildcard reports an unconstrained CPE version component.
func wildcard(v string) bool { return v == "*" || v == "-" || v == "" }

// displayName turns a CPE vendor/product component into a name: underscores become spaces. It returns
// "" for names containing wildcards or characters the Security module would refuse.
func displayName(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "_", " "))
	if s == "" || len(s) > 200 || strings.ContainsAny(s, "*?") {
		return ""
	}
	return s
}

// platformOf maps a CPE target software value to an Advisory OS platform ("" when unknown).
func platformOf(targetSW string) string {
	switch strings.ToLower(targetSW) {
	case "windows", "microsoft_windows":
		return "windows"
	case "macos", "mac_os_x", "mac_os":
		return "macos"
	case "linux", "linux_kernel":
		return "linux"
	case "android":
		return "android"
	case "iphone_os", "ios":
		return "ios"
	}
	return ""
}
