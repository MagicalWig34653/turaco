package transport

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

type artifactDTO struct {
	ID                string  `json:"id"`
	Provider          string  `json:"provider"`
	ExternalID        string  `json:"externalId"`
	Kind              string  `json:"kind"`
	Name              string  `json:"name"`
	Platform          string  `json:"platform"`
	SoftwareProductID *string `json:"softwareProductId"`
	Revision          *string `json:"revision"`
	Source            string  `json:"source"`
	ObservedAt        string  `json:"observedAt"`
	LastSyncedAt      string  `json:"lastSyncedAt"`
	DeletedObservedAt *string `json:"deletedObservedAt"`
	Version           int     `json:"version"`
}

func toArtifact(a application.Artifact) artifactDTO {
	return artifactDTO{ID: a.ID, Provider: a.Provider, ExternalID: a.ExternalID, Kind: a.Kind, Name: a.Name, Platform: a.Platform,
		SoftwareProductID: a.SoftwareProductID, Revision: a.Revision, Source: a.Source, ObservedAt: ts(a.ObservedAt),
		LastSyncedAt: ts(a.LastSyncedAt), DeletedObservedAt: tsPtr(a.DeletedObservedAt), Version: a.Version}
}

type filterSummaryDTO struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Rule     string `json:"rule"`
	Deleted  bool   `json:"deleted"`
}

type assignmentDTO struct {
	ID                    string            `json:"id"`
	ProviderAssignmentID  string            `json:"providerAssignmentId"`
	TargetKind            string            `json:"targetKind"`
	TargetGroupExternalID *string           `json:"targetGroupExternalId"`
	Mode                  string            `json:"mode"`
	Intent                string            `json:"intent"`
	FilterMode            string            `json:"filterMode"`
	Filter                *filterSummaryDTO `json:"filter"`
	Current               bool              `json:"current"`
	Source                string            `json:"source"`
	ObservedAt            string            `json:"observedAt"`
	LastSyncedAt          string            `json:"lastSyncedAt"`
	ValidFrom             string            `json:"validFrom"`
	ValidUntil            *string           `json:"validUntil"`
}

func toAssignment(a application.Assignment) assignmentDTO {
	out := assignmentDTO{ID: a.ID, ProviderAssignmentID: a.ProviderAssignmentID, TargetKind: a.TargetKind, TargetGroupExternalID: a.TargetGroupExternalID,
		Mode: a.Mode, Intent: a.Intent, FilterMode: a.FilterMode, Current: a.Current(), Source: a.Source, ObservedAt: ts(a.ObservedAt),
		LastSyncedAt: ts(a.LastSyncedAt), ValidFrom: ts(a.ValidFrom), ValidUntil: tsPtr(a.ValidUntil)}
	if a.Filter != nil {
		out.Filter = &filterSummaryDTO{ID: a.Filter.ID, Name: a.Filter.Name, Platform: a.Filter.Platform, Rule: a.Filter.Rule, Deleted: a.Filter.Deleted}
	}
	return out
}

func validQuery(q string) bool {
	return utf8.RuneCountInString(q) <= 100 && utf8.ValidString(q) && !strings.ContainsRune(q, 0)
}

func (h *handler) listArtifacts(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	v := r.URL.Query()
	if !validQuery(v.Get("q")) {
		httpx.WriteError(w, http.StatusBadRequest, "endpoints.invalid_request", "The search query is invalid.")
		return
	}
	res, err := h.svc.ListArtifacts(r.Context(), principal(r), application.ArtifactFilter{
		Kind: v.Get("kind"), Platform: v.Get("platform"), Query: v.Get("q"), IncludeDeleted: v.Get("includeDeleted") == "true", Page: page})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := struct {
		Items      []artifactDTO `json:"items"`
		NextCursor string        `json:"nextCursor,omitempty"`
	}{Items: make([]artifactDTO, 0, len(res.Items)), NextCursor: res.NextCursor}
	for _, a := range res.Items {
		out.Items = append(out.Items, toArtifact(a))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *handler) getArtifact(w http.ResponseWriter, r *http.Request) {
	d, err := h.svc.GetArtifact(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := struct {
		artifactDTO
		Assignments       []assignmentDTO `json:"assignments"`
		ObservationCounts map[string]int  `json:"observationCounts"`
	}{artifactDTO: toArtifact(d.Artifact), Assignments: make([]assignmentDTO, 0, len(d.Assignments)), ObservationCounts: d.ObservationCounts}
	if out.ObservationCounts == nil {
		out.ObservationCounts = map[string]int{}
	}
	for _, a := range d.Assignments {
		out.Assignments = append(out.Assignments, toAssignment(a))
	}
	httpx.JSON(w, http.StatusOK, out)
}

type filterDTO struct {
	ID                string  `json:"id"`
	Provider          string  `json:"provider"`
	ExternalID        string  `json:"externalId"`
	Name              string  `json:"name"`
	Platform          string  `json:"platform"`
	Rule              string  `json:"rule"`
	Revision          *string `json:"revision"`
	Source            string  `json:"source"`
	ObservedAt        string  `json:"observedAt"`
	LastSyncedAt      string  `json:"lastSyncedAt"`
	DeletedObservedAt *string `json:"deletedObservedAt"`
}

func (h *handler) listFilters(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	v := r.URL.Query()
	if !validQuery(v.Get("q")) {
		httpx.WriteError(w, http.StatusBadRequest, "endpoints.invalid_request", "The search query is invalid.")
		return
	}
	res, err := h.svc.ListManagementFilters(r.Context(), principal(r), application.FilterListFilter{
		Platform: v.Get("platform"), Query: v.Get("q"), IncludeDeleted: v.Get("includeDeleted") == "true", Page: page})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := struct {
		Items      []filterDTO `json:"items"`
		NextCursor string      `json:"nextCursor,omitempty"`
	}{Items: make([]filterDTO, 0, len(res.Items)), NextCursor: res.NextCursor}
	for _, f := range res.Items {
		out.Items = append(out.Items, filterDTO{ID: f.ID, Provider: f.Provider, ExternalID: f.ExternalID, Name: f.Name, Platform: f.Platform, Rule: f.Rule,
			Revision: f.Revision, Source: f.Source, ObservedAt: ts(f.ObservedAt), LastSyncedAt: ts(f.LastSyncedAt), DeletedObservedAt: tsPtr(f.DeletedObservedAt)})
	}
	httpx.JSON(w, http.StatusOK, out)
}

type observationDTO struct {
	ID              string `json:"id"`
	ArtifactID      string `json:"artifactId"`
	ArtifactName    string `json:"artifactName"`
	ArtifactKind    string `json:"artifactKind"`
	ArtifactDeleted bool   `json:"artifactDeleted"`
	NormalizedState string `json:"normalizedState"`
	RawStatus       string `json:"rawStatus"`
	Source          string `json:"source"`
	ObservedAt      string `json:"observedAt"`
	LastSyncedAt    string `json:"lastSyncedAt"`
}

func (h *handler) deviceObservations(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	res, err := h.svc.ListDeviceObservations(r.Context(), principal(r), r.PathValue("id"), page)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := struct {
		Items      []observationDTO `json:"items"`
		NextCursor string           `json:"nextCursor,omitempty"`
	}{Items: make([]observationDTO, 0, len(res.Items)), NextCursor: res.NextCursor}
	for _, o := range res.Items {
		out.Items = append(out.Items, observationDTO{ID: o.ID, ArtifactID: o.ArtifactID, ArtifactName: o.ArtifactName, ArtifactKind: o.ArtifactKind,
			ArtifactDeleted: o.ArtifactDeleted, NormalizedState: o.NormalizedState, RawStatus: o.RawStatus, Source: o.Source,
			ObservedAt: ts(o.ObservedAt), LastSyncedAt: ts(o.LastSyncedAt)})
	}
	httpx.JSON(w, http.StatusOK, out)
}
