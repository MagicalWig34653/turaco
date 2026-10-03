package transport

import (
	"net/http"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/endpoints/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

// Management views (F6 slice 3): Assigned, Expected Applicable and Observed are separate fields everywhere.

type groupRefDTO struct {
	ExternalID *string `json:"externalId"`
	Name       *string `json:"name"`
	Redacted   bool    `json:"redacted"`
}

func toGroupRef(g *application.GroupRef) *groupRefDTO {
	if g == nil {
		return nil
	}
	return &groupRefDTO{ExternalID: g.ExternalID, Name: g.Name, Redacted: g.Redacted}
}

type userRefDTO struct {
	ID       *string `json:"id"`
	Name     *string `json:"name"`
	Redacted bool    `json:"redacted"`
}

func toUserRef(u *application.UserRef) *userRefDTO {
	if u == nil {
		return nil
	}
	return &userRefDTO{ID: u.ID, Name: u.Name, Redacted: u.Redacted}
}

type expectedDTO struct {
	Result      string   `json:"result"`
	Confidence  string   `json:"confidence"`
	Reasons     []string `json:"reasons"`
	EvaluatedAt string   `json:"evaluatedAt"`
}

func toExpected(e application.ExpectedApplicability) expectedDTO {
	reasons := e.Reasons
	if reasons == nil {
		reasons = []string{}
	}
	return expectedDTO{Result: e.Result, Confidence: e.Confidence, Reasons: reasons, EvaluatedAt: ts(e.EvaluatedAt)}
}

type observedDTO struct {
	State        string `json:"state"`
	RawStatus    string `json:"rawStatus"`
	Source       string `json:"source"`
	ObservedAt   string `json:"observedAt"`
	LastSyncedAt string `json:"lastSyncedAt"`
	Stale        bool   `json:"stale"`
}

func toObserved(o *application.ObservedState) *observedDTO {
	if o == nil {
		return nil
	}
	return &observedDTO{State: o.State, RawStatus: o.RawStatus, Source: o.Source, ObservedAt: ts(o.ObservedAt), LastSyncedAt: ts(o.LastSyncedAt), Stale: o.Stale}
}

type assignedTargetDTO struct {
	AssignmentID string            `json:"assignmentId"`
	TargetKind   string            `json:"targetKind"`
	Group        *groupRefDTO      `json:"group"`
	Mode         string            `json:"mode"`
	Intent       string            `json:"intent"`
	FilterMode   string            `json:"filterMode"`
	Filter       *filterSummaryDTO `json:"filter"`
	Match        string            `json:"match"`
	Origins      []string          `json:"origins"`
	Nested       bool              `json:"nested"`
	Source       string            `json:"source"`
	LastSyncedAt string            `json:"lastSyncedAt"`
}

func toAssignedTargets(in []application.AssignedTarget) []assignedTargetDTO {
	out := make([]assignedTargetDTO, 0, len(in))
	for _, a := range in {
		d := assignedTargetDTO{AssignmentID: a.AssignmentID, TargetKind: a.TargetKind, Group: toGroupRef(a.Group), Mode: a.Mode, Intent: a.Intent,
			FilterMode: a.FilterMode, Match: a.Match, Origins: a.Origins, Nested: a.Nested, Source: a.Source, LastSyncedAt: ts(a.LastSyncedAt)}
		if d.Origins == nil {
			d.Origins = []string{}
		}
		if f := a.Filter; f != nil {
			d.Filter = &filterSummaryDTO{ID: f.ID, Name: f.Name, Platform: f.Platform, Rule: f.Rule, Deleted: f.Deleted}
		}
		out = append(out, d)
	}
	return out
}

func (h *handler) deviceManagement(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := application.DeviceManagementFilter{Kind: q.Get("kind"), State: q.Get("state"), Expected: q.Get("expected"), Mismatch: q.Get("mismatch"), Page: page}
	res, err := h.svc.DeviceManagement(r.Context(), principal(r), r.PathValue("id"), f)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type item struct {
		Artifact    artifactDTO         `json:"artifact"`
		Assigned    bool                `json:"assigned"`
		Assignments []assignedTargetDTO `json:"assignments"`
		Expected    expectedDTO         `json:"expected"`
		Observed    *observedDTO        `json:"observed"`
		Mismatch    string              `json:"mismatch,omitempty"`
	}
	out := struct {
		Items      []item `json:"items"`
		NextCursor string `json:"nextCursor,omitempty"`
		Truncated  bool   `json:"truncated"`
	}{Items: make([]item, 0, len(res.Items)), NextCursor: res.NextCursor, Truncated: res.Truncated}
	for _, st := range res.Items {
		out.Items = append(out.Items, item{Artifact: toArtifact(st.Artifact), Assigned: st.Assigned, Assignments: toAssignedTargets(st.Assignments),
			Expected: toExpected(st.Expected), Observed: toObserved(st.Observed), Mismatch: st.Mismatch})
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *handler) assignmentPath(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.AssignmentPath(r.Context(), principal(r), r.PathValue("id"), r.PathValue("artifactId"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type step struct {
		Kind         string       `json:"kind"`
		Origin       string       `json:"origin"`
		Group        *groupRefDTO `json:"group"`
		AssignmentID string       `json:"assignmentId,omitempty"`
		Mode         string       `json:"mode,omitempty"`
		Intent       string       `json:"intent,omitempty"`
		TargetKind   string       `json:"targetKind,omitempty"`
		FilterResult string       `json:"filterResult,omitempty"`
		Result       string       `json:"result"`
	}
	out := struct {
		DeviceID    string              `json:"deviceId"`
		DeviceName  string              `json:"deviceName"`
		Artifact    artifactDTO         `json:"artifact"`
		User        *userRefDTO         `json:"user"`
		Expected    expectedDTO         `json:"expected"`
		Path        []step              `json:"path"`
		Assignments []assignedTargetDTO `json:"assignments"`
		Observed    *observedDTO        `json:"observed"`
	}{DeviceID: v.Device.ID, DeviceName: v.Device.Name, Artifact: toArtifact(v.Artifact), User: toUserRef(v.User), Expected: toExpected(v.Expected),
		Path: make([]step, 0, len(v.Path)), Assignments: toAssignedTargets(v.Assignments), Observed: toObserved(v.Observed)}
	for _, s := range v.Path {
		out.Path = append(out.Path, step{Kind: s.Kind, Origin: s.Origin, Group: toGroupRef(s.Group), AssignmentID: s.AssignmentID, Mode: s.Mode,
			Intent: s.Intent, TargetKind: s.TargetKind, FilterResult: s.FilterResult, Result: s.Result})
	}
	httpx.JSON(w, http.StatusOK, out)
}

type exampleDTO struct {
	DeviceID   string `json:"deviceId"`
	Name       string `json:"name"`
	Result     string `json:"result"`
	Confidence string `json:"confidence"`
	Observed   string `json:"observed"`
}

type evaluationDTO struct {
	Shown     bool           `json:"shown"`
	Evaluated int            `json:"evaluated"`
	Truncated bool           `json:"truncated"`
	Expected  map[string]int `json:"expected"`
	Observed  map[string]int `json:"observed"`
	Examples  []exampleDTO   `json:"examples"`
}

func toEvaluation(e application.Evaluation) evaluationDTO {
	out := evaluationDTO{Shown: e.Shown, Evaluated: e.Evaluated, Truncated: e.Truncated, Expected: map[string]int(e.Expected), Observed: e.Observed,
		Examples: make([]exampleDTO, 0, len(e.Examples))}
	if out.Expected == nil {
		out.Expected = map[string]int{}
	}
	if out.Observed == nil {
		out.Observed = map[string]int{}
	}
	for _, x := range e.Examples {
		out.Examples = append(out.Examples, exampleDTO{DeviceID: x.DeviceID, Name: x.Name, Result: x.Result, Confidence: x.Confidence, Observed: x.Observed})
	}
	return out
}

func (h *handler) groupManagement(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	g, err := h.svc.GroupManagement(r.Context(), principal(r), r.PathValue("id"), page)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type item struct {
		Artifact    artifactDTO         `json:"artifact"`
		Assignments []assignedTargetDTO `json:"assignments"`
		Evaluation  evaluationDTO       `json:"evaluation"`
	}
	out := struct {
		GroupID             string `json:"groupId"`
		ExternalID          string `json:"externalId"`
		Name                string `json:"name"`
		Items               []item `json:"items"`
		NextCursor          string `json:"nextCursor,omitempty"`
		CandidateDevices    int    `json:"candidateDevices"`
		CandidatesTruncated bool   `json:"candidatesTruncated"`
	}{GroupID: g.GroupID, ExternalID: g.ExternalID, Name: g.Name, Items: make([]item, 0, len(g.Items)), NextCursor: g.NextCursor,
		CandidateDevices: g.CandidateDevices, CandidatesTruncated: g.CandidatesTruncated}
	for _, it := range g.Items {
		out.Items = append(out.Items, item{Artifact: toArtifact(it.Artifact), Assignments: toAssignedTargets(it.Assignments), Evaluation: toEvaluation(it.Evaluation)})
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *handler) userManagement(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	u, err := h.svc.UserManagement(r.Context(), principal(r), r.PathValue("id"), page)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type device struct {
		DeviceID string       `json:"deviceId"`
		Name     string       `json:"name"`
		Expected expectedDTO  `json:"expected"`
		Observed *observedDTO `json:"observed"`
	}
	type item struct {
		Artifact   artifactDTO         `json:"artifact"`
		Targeting  []assignedTargetDTO `json:"targeting"`
		UserResult string              `json:"userResult"`
		Devices    []device            `json:"devices"`
	}
	out := struct {
		UserID           string `json:"userId"`
		Name             string `json:"name"`
		Items            []item `json:"items"`
		NextCursor       string `json:"nextCursor,omitempty"`
		DevicesShown     bool   `json:"devicesShown"`
		DevicesTruncated bool   `json:"devicesTruncated"`
	}{UserID: u.UserID, Name: u.Name, Items: make([]item, 0, len(u.Items)), NextCursor: u.NextCursor, DevicesShown: u.DevicesShown, DevicesTruncated: u.DevicesTruncated}
	for _, it := range u.Items {
		d := item{Artifact: toArtifact(it.Artifact), Targeting: toAssignedTargets(it.Targeting), UserResult: it.UserResult, Devices: make([]device, 0, len(it.Devices))}
		for _, x := range it.Devices {
			d.Devices = append(d.Devices, device{DeviceID: x.DeviceID, Name: x.Name, Expected: toExpected(x.Expected), Observed: toObserved(x.Observed)})
		}
		out.Items = append(out.Items, d)
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *handler) artifactTargets(w http.ResponseWriter, r *http.Request) {
	t, err := h.svc.ArtifactTargets(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := struct {
		Artifact      artifactDTO         `json:"artifact"`
		Assignments   []assignedTargetDTO `json:"assignments"`
		Evaluation    evaluationDTO       `json:"evaluation"`
		ObservedTotal map[string]int      `json:"observedTotal"`
	}{Artifact: toArtifact(t.Artifact), Assignments: toAssignedTargets(t.Assignments), Evaluation: toEvaluation(t.Evaluation), ObservedTotal: t.ObservedTotal}
	if out.ObservedTotal == nil {
		out.ObservedTotal = map[string]int{}
	}
	httpx.JSON(w, http.StatusOK, out)
}
