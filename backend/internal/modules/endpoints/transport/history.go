package transport

import (
	"net/http"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

// History and diff (F6 slice 4). History lists meaningful changes only, newest first, with source and the provider
// observation time of each entry. Diffs keep Assigned, Expected Applicable and Observed apart per side and leave
// unknowns unknown.

type historyArtifactDTO struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Deleted bool   `json:"deleted"`
}

type assignmentSnapshotDTO struct {
	TargetKind string       `json:"targetKind"`
	Group      *groupRefDTO `json:"group"`
	Mode       string       `json:"mode"`
	Intent     string       `json:"intent"`
	FilterMode string       `json:"filterMode"`
	FilterID   *string      `json:"filterId"`
	FilterName *string      `json:"filterName"`
}

func toSnapshot(a application.AssignmentSnapshot) assignmentSnapshotDTO {
	return assignmentSnapshotDTO{TargetKind: a.TargetKind, Group: toGroupRef(a.Group), Mode: a.Mode, Intent: a.Intent, FilterMode: a.FilterMode, FilterID: a.FilterID, FilterName: a.FilterName}
}

type historyEntryDTO struct {
	Kind       string              `json:"kind"`
	OccurredAt string              `json:"occurredAt"`
	Source     string              `json:"source"`
	ObservedAt string              `json:"observedAt"`
	Artifact   *historyArtifactDTO `json:"artifact,omitempty"`
	Assignment *struct {
		AssignmentID         string                 `json:"assignmentId"`
		ProviderAssignmentID string                 `json:"providerAssignmentId"`
		Current              assignmentSnapshotDTO  `json:"current"`
		Previous             *assignmentSnapshotDTO `json:"previous"`
		Changes              []string               `json:"changes"`
	} `json:"assignment,omitempty"`
	Observation *struct {
		State         string `json:"state"`
		PreviousState string `json:"previousState,omitempty"`
		RawStatus     string `json:"rawStatus"`
	} `json:"observation,omitempty"`
	Group *groupRefDTO `json:"group,omitempty"`
}

func toHistory(in []application.HistoryEntry) []historyEntryDTO {
	out := make([]historyEntryDTO, 0, len(in))
	for _, e := range in {
		d := historyEntryDTO{Kind: e.Kind, OccurredAt: ts(e.OccurredAt), Source: e.Source, ObservedAt: ts(e.ObservedAt), Group: toGroupRef(e.Group)}
		if a := e.Artifact; a != nil {
			d.Artifact = &historyArtifactDTO{ID: a.ID, Name: a.Name, Kind: a.Kind, Deleted: a.Deleted}
		}
		if a := e.Assignment; a != nil {
			d.Assignment = &struct {
				AssignmentID         string                 `json:"assignmentId"`
				ProviderAssignmentID string                 `json:"providerAssignmentId"`
				Current              assignmentSnapshotDTO  `json:"current"`
				Previous             *assignmentSnapshotDTO `json:"previous"`
				Changes              []string               `json:"changes"`
			}{AssignmentID: a.AssignmentID, ProviderAssignmentID: a.ProviderAssignmentID, Current: toSnapshot(a.Current), Changes: a.Changes}
			if d.Assignment.Changes == nil {
				d.Assignment.Changes = []string{}
			}
			if a.Previous != nil {
				p := toSnapshot(*a.Previous)
				d.Assignment.Previous = &p
			}
		}
		if o := e.Observation; o != nil {
			d.Observation = &struct {
				State         string `json:"state"`
				PreviousState string `json:"previousState,omitempty"`
				RawStatus     string `json:"rawStatus"`
			}{State: o.State, PreviousState: o.PreviousState, RawStatus: o.RawStatus}
		}
		out = append(out, d)
	}
	return out
}

func (h *handler) artifactHistory(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	res, err := h.svc.ArtifactHistory(r.Context(), principal(r), r.PathValue("id"), page)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Artifact   artifactDTO       `json:"artifact"`
		Items      []historyEntryDTO `json:"items"`
		NextCursor string            `json:"nextCursor,omitempty"`
	}{Artifact: toArtifact(res.Artifact), Items: toHistory(res.Items), NextCursor: res.NextCursor})
}

func (h *handler) deviceHistory(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	res, err := h.svc.DeviceHistory(r.Context(), principal(r), r.PathValue("id"), page)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		DeviceID        string            `json:"deviceId"`
		AssignmentScope string            `json:"assignmentScope"`
		Items           []historyEntryDTO `json:"items"`
		NextCursor      string            `json:"nextCursor,omitempty"`
	}{DeviceID: res.DeviceID, AssignmentScope: res.AssignmentScope, Items: toHistory(res.Items), NextCursor: res.NextCursor})
}

func parseDiffFilter(w http.ResponseWriter, r *http.Request) (application.DiffFilter, bool) {
	page, ok := parsePage(w, r)
	if !ok {
		return application.DiffFilter{}, false
	}
	v := r.URL.Query()
	f := application.DiffFilter{Kind: v.Get("kind"), Page: page}
	switch v.Get("differences") {
	case "", "false":
	case "true":
		f.OnlyDifferences = true
	default:
		httpx.WriteError(w, http.StatusBadRequest, "endpoints.invalid_request", "differences must be true or false.")
		return application.DiffFilter{}, false
	}
	return f, true
}

type diffSideDTO struct {
	Assigned        bool                `json:"assigned"`
	AssignedUnknown bool                `json:"assignedUnknown"`
	Assignments     []assignedTargetDTO `json:"assignments"`
	Expected        expectedDTO         `json:"expected"`
	Observed        *observedDTO        `json:"observed"`
	Mismatch        string              `json:"mismatch,omitempty"`
}

func toDiffSide(s application.DiffSide) diffSideDTO {
	return diffSideDTO{Assigned: s.Assigned, AssignedUnknown: s.AssignedUnknown, Assignments: toAssignedTargets(s.Assignments), Expected: toExpected(s.Expected),
		Observed: toObserved(s.Observed), Mismatch: s.Mismatch}
}

func (h *handler) deviceDiff(w http.ResponseWriter, r *http.Request) {
	f, ok := parseDiffFilter(w, r)
	if !ok {
		return
	}
	res, err := h.svc.DeviceDiff(r.Context(), principal(r), r.PathValue("id"), r.URL.Query().Get("otherDeviceId"), f)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type item struct {
		Artifact   artifactDTO `json:"artifact"`
		Left       diffSideDTO `json:"left"`
		Right      diffSideDTO `json:"right"`
		Class      string      `json:"class"`
		Dimensions struct {
			Assigned string `json:"assigned"`
			Expected string `json:"expected"`
			Observed string `json:"observed"`
		} `json:"dimensions"`
		Uncertain bool `json:"uncertain"`
	}
	out := struct {
		Left       map[string]string `json:"left"`
		Right      map[string]string `json:"right"`
		Items      []item            `json:"items"`
		NextCursor string            `json:"nextCursor,omitempty"`
		Truncated  bool              `json:"truncated"`
	}{Left: map[string]string{"deviceId": res.LeftID, "name": res.LeftName}, Right: map[string]string{"deviceId": res.RightID, "name": res.RightName},
		Items: make([]item, 0, len(res.Items)), NextCursor: res.NextCursor, Truncated: res.Truncated}
	for _, it := range res.Items {
		d := item{Artifact: toArtifact(it.Artifact), Left: toDiffSide(it.Left), Right: toDiffSide(it.Right), Class: it.Class, Uncertain: it.Uncertain}
		d.Dimensions.Assigned, d.Dimensions.Expected, d.Dimensions.Observed = it.Dimensions.Assigned, it.Dimensions.Expected, it.Dimensions.Observed
		out.Items = append(out.Items, d)
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *handler) groupDiff(w http.ResponseWriter, r *http.Request) {
	f, ok := parseDiffFilter(w, r)
	if !ok {
		return
	}
	res, err := h.svc.GroupDiff(r.Context(), principal(r), r.PathValue("id"), r.URL.Query().Get("otherGroupId"), f)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type ref struct {
		GroupID    string `json:"groupId"`
		ExternalID string `json:"externalId"`
		Name       string `json:"name"`
	}
	type item struct {
		Artifact    artifactDTO         `json:"artifact"`
		Left        []assignedTargetDTO `json:"left"`
		Right       []assignedTargetDTO `json:"right"`
		Class       string              `json:"class"`
		Differences []string            `json:"differences"`
	}
	out := struct {
		Left       ref    `json:"left"`
		Right      ref    `json:"right"`
		Items      []item `json:"items"`
		NextCursor string `json:"nextCursor,omitempty"`
		Truncated  bool   `json:"truncated"`
	}{Left: ref{res.Left.ID, res.Left.ExternalID, res.Left.Name}, Right: ref{res.Right.ID, res.Right.ExternalID, res.Right.Name},
		Items: make([]item, 0, len(res.Items)), NextCursor: res.NextCursor, Truncated: res.Truncated}
	for _, it := range res.Items {
		diffs := it.Differences
		if diffs == nil {
			diffs = []string{}
		}
		out.Items = append(out.Items, item{Artifact: toArtifact(it.Artifact), Left: toAssignedTargets(it.Left.Assignments), Right: toAssignedTargets(it.Right.Assignments),
			Class: it.Class, Differences: diffs})
	}
	httpx.JSON(w, http.StatusOK, out)
}
