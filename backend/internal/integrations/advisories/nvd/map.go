package nvd

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/advisories"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// Source is the advisory source key of this adapter.
const Source = "nvd"

// Bounds that keep a record inside the limits the Security module enforces.
const (
	maxTitle      = 300
	maxTitleText  = 200
	maxSummary    = 3900
	maxCriteria   = 50
	maxRules      = 20
	maxReferences = 10
	maxRefLength  = 500
)

var cveIDPattern = regexp.MustCompile(`^CVE-[0-9]{4}-[0-9]{4,12}$`)

// The NVD API 2.0 response, reduced to the fields the mapping reads. Unknown fields are ignored.
type response struct {
	ResultsPerPage  int             `json:"resultsPerPage"`
	StartIndex      int             `json:"startIndex"`
	TotalResults    int             `json:"totalResults"`
	Vulnerabilities []vulnerability `json:"vulnerabilities"`
}

type vulnerability struct {
	CVE cve `json:"cve"`
}

type cve struct {
	ID             string          `json:"id"`
	Published      string          `json:"published"`
	LastModified   string          `json:"lastModified"`
	VulnStatus     string          `json:"vulnStatus"`
	Descriptions   []langString    `json:"descriptions"`
	Metrics        metrics         `json:"metrics"`
	Configurations []configuration `json:"configurations"`
	References     []reference     `json:"references"`
}

type langString struct {
	Lang  string `json:"lang"`
	Value string `json:"value"`
}

type metrics struct {
	V40 []metric `json:"cvssMetricV40"`
	V31 []metric `json:"cvssMetricV31"`
	V30 []metric `json:"cvssMetricV30"`
}

type metric struct {
	Type     string `json:"type"`
	CVSSData struct {
		BaseScore *float64 `json:"baseScore"`
	} `json:"cvssData"`
}

type configuration struct {
	Nodes []node `json:"nodes"`
}

type node struct {
	Negate   bool       `json:"negate"`
	CPEMatch []cpeMatch `json:"cpeMatch"`
}

type cpeMatch struct {
	Vulnerable            bool   `json:"vulnerable"`
	Criteria              string `json:"criteria"`
	VersionStartIncluding string `json:"versionStartIncluding"`
	VersionStartExcluding string `json:"versionStartExcluding"`
	VersionEndIncluding   string `json:"versionEndIncluding"`
	VersionEndExcluding   string `json:"versionEndExcluding"`
}

type reference struct {
	URL string `json:"url"`
}

// toRecord maps one CVE; ok is false for CVEs that must not be imported (rejected or malformed id).
func toRecord(c cve) (advisories.AdvisoryRecord, bool) {
	id := strings.ToUpper(strings.TrimSpace(c.ID))
	if !cveIDPattern.MatchString(id) || strings.EqualFold(c.VulnStatus, "Rejected") {
		return advisories.AdvisoryRecord{}, false
	}
	desc := ""
	for _, d := range c.Descriptions {
		if strings.EqualFold(d.Lang, "en") {
			desc = d.Value
			break
		}
	}
	rec := advisories.AdvisoryRecord{
		Source: Source, ExternalID: id, Title: title(id, desc), Summary: summary(desc),
		Severity: severity(c.Metrics), PublishedAt: parseTime(c.Published), ModifiedAt: parseTime(c.LastModified),
		SourceURL: "https://nvd.nist.gov/vuln/detail/" + id, Criteria: criteria(c.Configurations),
		References: references(c.References),
	}
	return rec, true
}

// parseTime reads the NVD timestamp formats (UTC without zone, optionally with milliseconds).
func parseTime(s string) *time.Time {
	for _, layout := range []string{"2006-01-02T15:04:05.000", "2006-01-02T15:04:05", time.RFC3339Nano} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			t = t.UTC()
			return &t
		}
	}
	return nil
}

// clean removes characters the Security module refuses; with multiline, line breaks and tabs stay.
func clean(s string, multiline bool) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, " ")
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case multiline && (r == '\n' || r == '\t'):
			b.WriteRune(r)
		case r == '\r':
		case safetext.Unsafe(r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

func truncate(s string, runes int) string {
	if utf8.RuneCountInString(s) <= runes {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:runes-3])) + "..."
}

func title(id, desc string) string {
	line := strings.Join(strings.Fields(clean(desc, false)), " ")
	if line == "" {
		return id
	}
	return truncate(id+": "+truncate(line, maxTitleText), maxTitle)
}

func summary(desc string) string { return truncate(clean(desc, true), maxSummary) }

// severity maps the CVSS base score of the preferred metric (v4.0, then v3.1, then v3.0) to the
// Advisory severity. A CVE without a score (not analyzed yet) is "none".
func severity(m metrics) string {
	for _, list := range [][]metric{m.V40, m.V31, m.V30} {
		if score, ok := pick(list); ok {
			switch {
			case score >= 9.0:
				return "critical"
			case score >= 7.0:
				return "high"
			case score >= 4.0:
				return "medium"
			case score > 0:
				return "low"
			}
			return "none"
		}
	}
	return "none"
}

// pick prefers the Primary (NVD) metric and falls back to the first scored one.
func pick(list []metric) (float64, bool) {
	for _, m := range list {
		if m.Type == "Primary" && m.CVSSData.BaseScore != nil {
			return *m.CVSSData.BaseScore, true
		}
	}
	for _, m := range list {
		if m.CVSSData.BaseScore != nil {
			return *m.CVSSData.BaseScore, true
		}
	}
	return 0, false
}

func references(in []reference) []string {
	var out []string
	for _, r := range in {
		u := strings.TrimSpace(r.URL)
		if len(u) > maxRefLength || !strings.HasPrefix(u, "https://") || strings.ContainsAny(u, " \t\r\n\\\"<>`") || safetext.ContainsUnsafe(u, false) {
			continue
		}
		if p, err := url.Parse(u); err != nil || p.Host == "" || p.User != nil || slices.Contains(out, u) {
			continue
		}
		out = append(out, u)
		if len(out) == maxReferences {
			break
		}
	}
	return out
}

type groupKey struct{ vendor, product, platform string }

type group struct {
	key         groupKey
	rules       []advisories.VersionRule
	unconstrain bool
}

// criteria maps the vulnerable application CPE matches to Criteria, one per vendor/product/platform.
// Hardware (h) and operating system (o) parts and non-vulnerable (context) matches are skipped, as are
// negated nodes and matches whose versions the version rules cannot carry. Rules are alternatives: a
// product with an unconstrained match is affected in every version (no rules).
func criteria(configs []configuration) []advisories.Criteria {
	var groups []*group
	index := map[groupKey]*group{}
	for _, cfg := range configs {
		for _, n := range cfg.Nodes {
			if n.Negate {
				continue
			}
			for _, m := range n.CPEMatch {
				if !m.Vulnerable {
					continue
				}
				c, ok := parseCPE23(m.Criteria)
				if !ok || c.part != "a" {
					continue
				}
				rules, unconstrained, ok := rulesOf(m, c)
				if !ok {
					continue
				}
				key := groupKey{displayName(c.vendor), displayName(c.product), platformOf(c.targetSW)}
				if key.product == "" {
					continue
				}
				g := index[key]
				if g == nil {
					if len(groups) == maxCriteria {
						continue
					}
					g = &group{key: key}
					index[key] = g
					groups = append(groups, g)
				}
				if unconstrained {
					g.unconstrain = true
				}
				for _, r := range rules {
					if !slices.Contains(g.rules, r) && len(g.rules) < maxRules {
						g.rules = append(g.rules, r)
					}
				}
			}
		}
	}
	var out []advisories.Criteria
	for _, g := range groups {
		cr := advisories.Criteria{ProductName: g.key.product, Publisher: g.key.vendor, OSPlatform: g.key.platform}
		if !g.unconstrain {
			cr.Rules = g.rules
		}
		out = append(out, cr)
	}
	return out
}

// rulesOf maps the version fields of a cpeMatch. Ranges map to introduced/fixed (start inclusive, end
// exclusive) and to lt/le; a start-exclusive bound is treated as inclusive and a range with an inclusive
// end becomes "le end" (both over-report rather than miss an installation). A version that cannot be
// carried (unusual characters) skips the whole match.
func rulesOf(m cpeMatch, c cpe) (rules []advisories.VersionRule, unconstrained, ok bool) {
	start, startSet := m.VersionStartIncluding, m.VersionStartIncluding != ""
	if !startSet {
		start, startSet = m.VersionStartExcluding, m.VersionStartExcluding != ""
	}
	end, endExcl := m.VersionEndExcluding, true
	endSet := end != ""
	if !endSet {
		end, endExcl, endSet = m.VersionEndIncluding, false, m.VersionEndIncluding != ""
	}
	for _, v := range []string{start, end} {
		if v != "" && !versionPattern.MatchString(v) {
			return nil, false, false
		}
	}
	switch {
	case startSet && endSet && endExcl:
		return []advisories.VersionRule{{Kind: advisories.RuleIntroduced, Version: start}, {Kind: advisories.RuleFixed, Version: end}}, false, true
	case endSet && endExcl:
		return []advisories.VersionRule{{Kind: advisories.RuleLT, Version: end}}, false, true
	case endSet:
		return []advisories.VersionRule{{Kind: advisories.RuleLE, Version: end}}, false, true
	case startSet:
		return []advisories.VersionRule{{Kind: advisories.RuleIntroduced, Version: start}}, false, true
	case wildcard(c.version):
		return nil, true, true
	case versionPattern.MatchString(c.version):
		return []advisories.VersionRule{{Kind: advisories.RuleEQ, Version: c.version}}, false, true
	}
	return nil, false, false
}
