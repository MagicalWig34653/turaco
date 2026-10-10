package msrc

import (
	"context"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/integrations/advisories"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
)

// The CVRF JSON shape (field names as published in the MSRC API documentation).
type textValue struct {
	Value string `json:"Value"`
}

type cvrfDoc struct {
	DocumentTitle textValue `json:"DocumentTitle"`
	ProductTree   struct {
		FullProductName []struct {
			ProductID string `json:"ProductID"`
			Value     string `json:"Value"`
		} `json:"FullProductName"`
	} `json:"ProductTree"`
	Vulnerability []struct {
		CVE   string    `json:"CVE"`
		Title textValue `json:"Title"`
		Notes []struct {
			Title string `json:"Title"`
			Type  int    `json:"Type"`
			Value string `json:"Value"`
		} `json:"Notes"`
		RevisionHistory []struct {
			Date string `json:"Date"`
		} `json:"RevisionHistory"`
		Threats []struct {
			Type        int       `json:"Type"`
			Description textValue `json:"Description"`
			ProductID   []string  `json:"ProductID"`
		} `json:"Threats"`
		ProductStatuses []struct {
			Type      int      `json:"Type"`
			ProductID []string `json:"ProductID"`
		} `json:"ProductStatuses"`
		Remediations []struct {
			Type        int       `json:"Type"`
			Description textValue `json:"Description"`
			FixedBuild  string    `json:"FixedBuild"`
			ProductID   []string  `json:"ProductID"`
		} `json:"Remediations"`
	} `json:"Vulnerability"`
}

// CVRF constants used below: ProductStatuses Type 3 = Known Affected; Threats Type 3 = Severity; Notes Type 1 =
// Description; Remediations Type 2 = Vendor Fix.
const (
	statusKnownAffected = 3
	threatSeverity      = 3
	noteDescription     = 1
	remediationVendor   = 2
)

var htmlTag = regexp.MustCompile(`<[^>]*>`)

func (c *Client) document(ctx context.Context, id string) ([]advisories.AdvisoryRecord, error) {
	var d cvrfDoc
	if err := c.get(ctx, "/cvrf/"+url.PathEscape(id), &d); err != nil {
		return nil, err
	}
	if len(d.Vulnerability) > maxVulnsPerDoc {
		return nil, advisories.ErrInvalidResponse
	}
	names := map[string]string{}
	for _, p := range d.ProductTree.FullProductName {
		if productIDText.MatchString(p.ProductID) {
			names[p.ProductID] = truncate(oneLine(p.Value), 200)
		}
	}
	var out []advisories.AdvisoryRecord
	seen := map[string]bool{}
	for _, v := range d.Vulnerability {
		cve := strings.ToUpper(strings.TrimSpace(v.CVE))
		if !cveIDPattern.MatchString(cve) || seen[cve] {
			continue
		}
		seen[cve] = true
		rec := advisories.AdvisoryRecord{
			Source: SourceKey, ExternalID: cve, Title: truncate(oneLine(v.Title.Value), maxTitle),
			SourceURL: "https://msrc.microsoft.com/update-guide/vulnerability/" + url.PathEscape(cve),
			Severity:  "none",
		}
		if rec.Title == "" {
			rec.Title = cve
		}
		for _, n := range v.Notes {
			if n.Type == noteDescription || strings.EqualFold(n.Title, "Description") {
				rec.Summary = truncate(clean(htmlTag.ReplaceAllString(n.Value, " "), true), maxSummary)
				break
			}
		}
		for _, t := range v.Threats {
			if t.Type == threatSeverity {
				if sev := severity(t.Description.Value); rank(sev) > rank(rec.Severity) {
					rec.Severity = sev
				}
			}
		}
		for _, h := range v.RevisionHistory {
			if t := parseTime(h.Date); t != nil {
				if rec.PublishedAt == nil || t.Before(*rec.PublishedAt) {
					rec.PublishedAt = t
				}
				if rec.ModifiedAt == nil || t.After(*rec.ModifiedAt) {
					rec.ModifiedAt = t
				}
			}
		}
		// Fixed builds per product from the vendor-fix remediations.
		fixed := map[string]string{}
		for _, r := range v.Remediations {
			if r.Type != remediationVendor || !buildPattern.MatchString(strings.TrimSpace(r.FixedBuild)) {
				continue
			}
			for _, pid := range r.ProductID {
				if _, ok := fixed[pid]; !ok {
					fixed[pid] = strings.TrimSpace(r.FixedBuild)
				}
			}
		}
		var affected []string
		for _, st := range v.ProductStatuses {
			if st.Type == statusKnownAffected {
				affected = append(affected, st.ProductID...)
			}
		}
		sort.Strings(affected)
		count := 0
		for i, pid := range affected {
			if i > 0 && affected[i-1] == pid {
				continue
			}
			name, ok := names[pid]
			if !ok || name == "" {
				continue
			}
			if count >= maxProductsEach {
				rec.CriteriaSkipped++
				continue
			}
			count++
			cr := advisories.Criteria{ProductName: name, Publisher: "Microsoft", OSPlatform: platformOf(name)}
			if b, ok := fixed[pid]; ok {
				cr.Rules = []advisories.VersionRule{{Kind: advisories.RuleFixed, Version: b}}
			}
			rec.Criteria = append(rec.Criteria, cr)
		}
		out = append(out, rec)
	}
	return out, nil
}

func severity(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "critical":
		return "critical"
	case "important":
		return "high"
	case "moderate":
		return "medium"
	case "low":
		return "low"
	}
	return "none"
}

func rank(sev string) int {
	switch sev {
	case "critical":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	}
	return 0
}

func platformOf(product string) string {
	if strings.Contains(strings.ToLower(product), "windows") {
		return "windows"
	}
	return ""
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

func oneLine(s string) string { return strings.Join(strings.Fields(clean(s, false)), " ") }

func truncate(s string, runes int) string {
	if utf8.RuneCountInString(s) <= runes {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:runes-3])) + "..."
}
