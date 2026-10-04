// Package public is Security's read contract for other modules. Callers authorize the requesting
// user before selecting detail scope; no repository or Security-owned table crosses this boundary.
package public

import (
	"context"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/security/application"
)

// ReadScope selects the fields the caller may show. Zero scope exposes only reference and state.
type ReadScope struct{ IncludeDetails bool }

type AdvisoryInfo struct {
	ID              string
	Reference       string
	Status          string
	Severity        string
	Title           string
	Summary         *string
	AffectedDevices int
	MatchTruncated  bool
}

type Advisories struct{ service *application.Service }

func NewAdvisories(service *application.Service) *Advisories { return &Advisories{service: service} }

// Lookup returns a single Advisory's current state and affected count. The caller must check
// security.view before requesting details; a zero scope omits sensitive advisory text and counts.
func (a *Advisories) Lookup(ctx context.Context, id string, scope ReadScope) (AdvisoryInfo, error) {
	p := application.Principal{View: true}
	detail, err := a.service.GetAdvisory(ctx, p, id)
	if err != nil {
		return AdvisoryInfo{}, err
	}
	out := AdvisoryInfo{ID: detail.ID, Reference: detail.Reference, Status: detail.Status}
	if scope.IncludeDetails {
		summary, err := a.service.Summary(ctx, p, id)
		if err != nil {
			return AdvisoryInfo{}, err
		}
		out.Title, out.Summary, out.Severity = detail.Title, detail.Summary, detail.Severity
		out.AffectedDevices, out.MatchTruncated = summary.AffectedDevices, detail.MatchTruncated
	}
	return out, nil
}
