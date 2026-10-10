package osv

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
const Source = "osv"

// Bounds that keep a record inside the limits the Security module enforces.
const (
	maxTitle      = 300
	maxTitleText  = 200
	maxSummary    = 3900
	maxCriteria   = 50
	maxRules      = 20
	maxReferences = 10
	maxRefLength  = 500
	maxAliases    = 10
)

var (
	idPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{2,99}$`)
	versionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+~-]{0,99}$`)
	ecosystemRe    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._:-]{0,59}$`)
)

// validPackage reports whether a package query is well formed (it is sent as JSON, never as a URL).
func validPackage(p advisories.PackageQuery) bool {
	return ecosystemRe.MatchString(p.Ecosystem) && p.Name != "" && len(p.Name) <= 200 && utf8.ValidString(p.Name) &&
		!safetext.ContainsUnsafe(p.Name, false) && p.Name == strings.TrimSpace(p.Name)
}

// The OSV schema (https://ossf.github.io/osv-schema/), reduced to the fields the mapping reads. Unknown
// fields are ignored.
type queryResponse struct {
	Vulns         []vuln `json:"vulns"`
	NextPageToken string `json:"next_page_token"`
}

type vuln struct {
	ID               string           `json:"id"`
	Modified         string           `json:"modified"`
	Published        string           `json:"published"`
	Withdrawn        string           `json:"withdrawn"`
	Aliases          []string         `json:"aliases"`
	Summary          string           `json:"summary"`
	Details          string           `json:"details"`
	References       []reference      `json:"references"`
	Affected         []affected       `json:"affected"`
	DatabaseSpecific databaseSpecific `json:"database_specific"`
}

type databaseSpecific struct {
	Severity string `json:"severity"`
}

type reference struct {
	URL string `json:"url"`
}

type affected struct {
	Package  pkg      `json:"package"`
	Ranges   []rng    `json:"ranges"`
	Versions []string `json:"versions"`
}

type pkg struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
}

type rng struct {
	Type   string  `json:"type"`
	Events []event `json:"events"`
}

type event struct {
	Introduced   string `json:"introduced"`
	Fixed        string `json:"fixed"`
	LastAffected string `json:"last_affected"`
}

// toRecord maps one OSV record; ok is false for withdrawn records and malformed ids.
func toRecord(v vuln) (advisories.AdvisoryRecord, bool) {
	id := strings.TrimSpace(v.ID)
	if !idPattern.MatchString(id) || v.Withdrawn != "" {
		return advisories.AdvisoryRecord{}, false
	}
	text := v.Details
	if strings.TrimSpace(text) == "" {
		text = v.Summary
	}
	headline := v.Summary
	if strings.TrimSpace(headline) == "" {
		headline = text
	}
	crit, skipped := criteria(v.Affected)
	return advisories.AdvisoryRecord{
		Source: Source, ExternalID: id, Title: title(id, headline), Summary: summary(aliasLine(v.Aliases), text),
		Severity: severity(v.DatabaseSpecific.Severity), PublishedAt: parseTime(v.Published), ModifiedAt: parseTime(v.Modified),
		SourceURL: "https://osv.dev/vulnerability/" + url.PathEscape(id), Criteria: crit,
		CriteriaSkipped: skipped, References: references(v.References),
	}, true
}

// parseTime reads the RFC 3339 timestamps of OSV (UTC, optional fraction).
func parseTime(s string) *time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return nil
	}
	t = t.UTC()
	return &t
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

func title(id, headline string) string {
	line := strings.Join(strings.Fields(clean(headline, false)), " ")
	if line == "" {
		return id
	}
	return truncate(id+": "+truncate(line, maxTitleText), maxTitle)
}

// aliasLine lists the other ids of the same vulnerability (CVE, GHSA ...) so analysts can find it.
func aliasLine(aliases []string) string {
	var out []string
	for _, a := range aliases {
		a = strings.TrimSpace(a)
		if idPattern.MatchString(a) && !slices.Contains(out, a) {
			out = append(out, a)
		}
		if len(out) == maxAliases {
			break
		}
	}
	if len(out) == 0 {
		return ""
	}
	return "Aliases: " + strings.Join(out, ", ")
}

func summary(aliases, text string) string {
	text = clean(text, true)
	if aliases != "" {
		text = aliases + "\n\n" + text
	}
	return truncate(strings.TrimSpace(text), maxSummary)
}

// severity maps the database-specific severity label OSV sources such as GitHub publish. OSV carries CVSS
// vectors rather than scores; Turaco does not compute scores, so a record without a label is "none".
func severity(label string) string {
	switch strings.ToUpper(strings.TrimSpace(label)) {
	case "CRITICAL":
		return "critical"
	case "HIGH":
		return "high"
	case "MODERATE", "MEDIUM":
		return "medium"
	case "LOW":
		return "low"
	}
	return "none"
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

type group struct {
	name  string
	rules []advisories.VersionRule
	// unconstrained marks a package entry without ranges or versions: every version is affected.
	unconstrained bool
}

// criteria maps the affected packages to Criteria, one per package name. Rules are alternatives. Per
// range of type SEMVER or ECOSYSTEM, an introduced/fixed pair becomes an introduced and a fixed rule ("0"
// included, so several ranges stay correct), an introduced event without end an introduced rule and a
// last_affected event a "le" rule (the lower bound of that range is dropped, which over-reports rather
// than misses an installation); explicit
// versions become eq rules. GIT ranges and versions the rules cannot carry are counted in skipped (never
// dropped silently), as are packages and rules beyond the bounds.
func criteria(in []affected) (out []advisories.Criteria, skipped int) {
	var groups []*group
	index := map[string]*group{}
	for _, a := range in {
		name := strings.TrimSpace(a.Package.Name)
		if name == "" || len(name) > 200 || !utf8.ValidString(name) || safetext.ContainsUnsafe(name, false) {
			skipped++
			continue
		}
		g := index[name]
		if g == nil {
			if len(groups) == maxCriteria {
				skipped++
				continue
			}
			g = &group{name: name}
			index[name] = g
			groups = append(groups, g)
		}
		rules, n := rulesOf(a)
		skipped += n
		if len(rules) == 0 && n == 0 {
			g.unconstrained = true
		}
		for _, r := range rules {
			if slices.Contains(g.rules, r) {
				continue
			}
			if len(g.rules) < maxRules {
				g.rules = append(g.rules, r)
			} else {
				skipped++
			}
		}
	}
	for _, g := range groups {
		cr := advisories.Criteria{ProductName: g.name}
		if !g.unconstrained {
			cr.Rules = g.rules
		}
		out = append(out, cr)
	}
	return out, skipped
}

// rulesOf maps one affected entry and counts what it had to leave out.
func rulesOf(a affected) (rules []advisories.VersionRule, skipped int) {
	add := func(kind, v string) {
		v = strings.TrimSpace(v)
		if !versionPattern.MatchString(v) {
			skipped++
			return
		}
		rules = append(rules, advisories.VersionRule{Kind: kind, Version: v})
	}
	for _, r := range a.Ranges {
		if r.Type != "SEMVER" && r.Type != "ECOSYSTEM" {
			skipped++
			continue
		}
		pending := "" // an introduced version waiting for its end event
		for _, e := range r.Events {
			switch {
			case e.Introduced != "":
				if pending != "" {
					add(advisories.RuleIntroduced, pending)
				}
				pending = strings.TrimSpace(e.Introduced)
			case e.Fixed != "":
				if pending != "" {
					add(advisories.RuleIntroduced, pending)
					pending = ""
				}
				add(advisories.RuleFixed, e.Fixed)
			case e.LastAffected != "":
				// The range keeps only its upper bound (over-reports, never misses an installation).
				pending = ""
				add(advisories.RuleLE, e.LastAffected)
			}
		}
		if pending != "" {
			add(advisories.RuleIntroduced, pending)
		}
	}
	for _, v := range a.Versions {
		add(advisories.RuleEQ, v)
	}
	return rules, skipped
}
