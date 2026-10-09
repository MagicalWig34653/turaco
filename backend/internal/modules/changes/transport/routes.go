package transport

import (
	"net/http"
	"strconv"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/changes/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

type changeDTO struct {
	ID                     string   `json:"id"`
	Reference              string   `json:"reference"`
	Title                  string   `json:"title"`
	Description            *string  `json:"description"`
	Kind                   string   `json:"kind"`
	Risk                   string   `json:"risk"`
	Status                 string   `json:"status"`
	StatusReason           *string  `json:"statusReason"`
	RequesterID            string   `json:"requesterId"`
	OwnerID                *string  `json:"ownerId"`
	RollbackPlan           *string  `json:"rollbackPlan"`
	EmergencyJustification *string  `json:"emergencyJustification"`
	WindowStart            *string  `json:"windowStart"`
	WindowEnd              *string  `json:"windowEnd"`
	OutcomeNote            *string  `json:"outcomeNote"`
	RollbackDone           *bool    `json:"rollbackDone"`
	ApprovalID             *string  `json:"approvalId"`
	ApprovedWindowStart    *string  `json:"approvedWindowStart"`
	ApprovedWindowEnd      *string  `json:"approvedWindowEnd"`
	EmergencyApprovedBy    *string  `json:"emergencyApprovedBy"`
	ReviewRequired         bool     `json:"reviewRequired"`
	StartedAt              *string  `json:"startedAt"`
	CompletedAt            *string  `json:"completedAt"`
	ClosedAt               *string  `json:"closedAt"`
	AllowedOperations      []string `json:"allowedOperations,omitempty"`
	Version                int      `json:"version"`
	CreatedAt              string   `json:"createdAt"`
	UpdatedAt              string   `json:"updatedAt"`
}

func toChange(c application.Change) changeDTO {
	return changeDTO{ID: c.ID, Reference: c.Reference, Title: c.Title, Description: c.Description, Kind: c.Kind, Risk: c.Risk, Status: c.Status,
		StatusReason: c.StatusReason, RequesterID: c.RequesterID, OwnerID: c.OwnerID, RollbackPlan: c.RollbackPlan,
		EmergencyJustification: c.EmergencyJustification, WindowStart: tsPtr(c.WindowStart), WindowEnd: tsPtr(c.WindowEnd),
		OutcomeNote: c.OutcomeNote, RollbackDone: c.RollbackDone, ApprovalID: c.ApprovalID, ApprovedWindowStart: tsPtr(c.ApprovedWindowStart), ApprovedWindowEnd: tsPtr(c.ApprovedWindowEnd),
		EmergencyApprovedBy: c.EmergencyApprovedBy, ReviewRequired: c.ReviewRequired(),
		StartedAt: tsPtr(c.StartedAt), CompletedAt: tsPtr(c.CompletedAt), ClosedAt: tsPtr(c.ClosedAt),
		Version: c.Version, CreatedAt: ts(c.CreatedAt), UpdatedAt: ts(c.UpdatedAt)}
}

type userNamesDTO struct {
	Users map[string]string `json:"users"`
}

type listResponse struct {
	Items      []changeDTO   `json:"items"`
	NextCursor string        `json:"nextCursor,omitempty"`
	Names      *userNamesDTO `json:"names,omitempty"`
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	from, ok := parseTime(w, q.Get("windowFrom"))
	if !ok {
		return
	}
	to, ok := parseTime(w, q.Get("windowTo"))
	if !ok {
		return
	}
	res, err := h.svc.List(r.Context(), principal(r), application.Filter{
		Status: q.Get("status"), Risk: q.Get("risk"), Kind: q.Get("kind"), OwnerID: q.Get("owner"), RequesterID: q.Get("requester"),
		AffectedType: q.Get("affectedType"), AffectedID: q.Get("affectedId"), WindowFrom: from, WindowTo: to, Page: page,
	})
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	out := listResponse{Items: make([]changeDTO, 0, len(res.Items)), NextCursor: res.NextCursor}
	var ids []string
	for _, c := range res.Items {
		out.Items = append(out.Items, toChange(c))
		ids = append(ids, c.RequesterID)
		if c.OwnerID != nil {
			ids = append(ids, *c.OwnerID)
		}
	}
	names, err := h.svc.UserNames(r.Context(), ids)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	out.Names = &userNamesDTO{Users: names}
	httpx.JSON(w, http.StatusOK, out)
}

type windowBody struct {
	Start *time.Time `json:"start"`
	End   *time.Time `json:"end"`
}

func (b *windowBody) window() application.Window {
	if b == nil {
		return application.Window{}
	}
	return application.Window{Start: b.Start, End: b.End}
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Title        string      `json:"title"`
		Description  string      `json:"description"`
		Kind         string      `json:"kind"`
		Risk         string      `json:"risk"`
		OwnerUserID  string      `json:"ownerUserId"`
		RollbackPlan string      `json:"rollbackPlan"`
		Window       *windowBody `json:"window"`
	}
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.Create(r.Context(), caller(w, r), principal(r), application.NewChange{Title: b.Title, Description: b.Description,
		Kind: b.Kind, Risk: b.Risk, OwnerUserID: b.OwnerUserID, RollbackPlan: b.RollbackPlan, Window: b.Window.window()})
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toChange(out))
}

func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int        `json:"expectedVersion"`
		Title           *string     `json:"title"`
		Description     *string     `json:"description"`
		Kind            *string     `json:"kind"`
		Risk            *string     `json:"risk"`
		OwnerUserID     *string     `json:"ownerUserId"`
		RollbackPlan    *string     `json:"rollbackPlan"`
		Window          *windowBody `json:"window"`
	}
	if !decode(w, r, &b) {
		return
	}
	in := application.Details{Title: b.Title, Description: b.Description, Kind: b.Kind, Risk: b.Risk, OwnerUserID: b.OwnerUserID, RollbackPlan: b.RollbackPlan}
	if b.Window != nil {
		wd := b.Window.window()
		in.Window = &wd
	}
	out, err := h.svc.UpdateDetails(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, in)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toChange(out))
}

// ---- detail ----

type affectedDTO struct {
	RelationshipID string  `json:"relationshipId"`
	Type           string  `json:"type"`
	ID             string  `json:"id"`
	Name           *string `json:"name"`
	Reference      *string `json:"reference"`
	Status         *string `json:"status"`
	Confidence     string  `json:"confidence"`
	Since          string  `json:"since"`
	Missing        bool    `json:"missing,omitempty"`
	Hidden         bool    `json:"hidden,omitempty"`
}

type approvalDTO struct {
	ID              string  `json:"id"`
	StepIndex       int     `json:"stepIndex"`
	Status          string  `json:"status"`
	ApproverUserID  *string `json:"approverUserId"`
	ApproverTeamID  *string `json:"approverTeamId"`
	DecidedByUserID *string `json:"decidedByUserId"`
	DecidedAt       *string `json:"decidedAt"`
}

type taskDTO struct {
	ID     string  `json:"id"`
	Title  string  `json:"title"`
	Status string  `json:"status"`
	DueAt  *string `json:"dueAt"`
}

type detailDTO struct {
	changeDTO
	Affected  []affectedDTO `json:"affected"`
	Approvals []approvalDTO `json:"approvals"`
	Tasks     struct {
		Total int       `json:"total"`
		Open  int       `json:"open"`
		Items []taskDTO `json:"items"`
	} `json:"tasks"`
	Names userNamesDTO `json:"names"`
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	d, err := h.svc.Get(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	out := detailDTO{changeDTO: toChange(d.Change), Affected: make([]affectedDTO, 0, len(d.Affected)), Approvals: make([]approvalDTO, 0, len(d.Approvals))}
	out.AllowedOperations = d.AllowedOps
	for _, a := range d.Affected {
		out.Affected = append(out.Affected, affectedDTO{RelationshipID: a.RelationshipID, Type: a.Type, ID: a.ID, Name: a.Name, Reference: a.Reference,
			Status: a.Status, Confidence: a.Confidence, Since: ts(a.Since), Missing: a.Missing, Hidden: a.Hidden})
	}
	ids := []string{d.Change.RequesterID}
	if d.Change.OwnerID != nil {
		ids = append(ids, *d.Change.OwnerID)
	}
	for _, a := range d.Approvals {
		out.Approvals = append(out.Approvals, approvalDTO{ID: a.ID, StepIndex: a.StepIndex, Status: a.Status, ApproverUserID: a.ApproverUserID,
			ApproverTeamID: a.ApproverTeamID, DecidedByUserID: a.DecidedByUserID, DecidedAt: tsPtr(a.DecidedAt)})
		for _, u := range []*string{a.ApproverUserID, a.DecidedByUserID} {
			if u != nil {
				ids = append(ids, *u)
			}
		}
	}
	out.Tasks.Total, out.Tasks.Open, out.Tasks.Items = d.Tasks.Total, d.Tasks.Open, make([]taskDTO, 0, len(d.Tasks.Items))
	for _, t := range d.Tasks.Items {
		out.Tasks.Items = append(out.Tasks.Items, taskDTO{ID: t.ID, Title: t.Title, Status: t.Status, DueAt: tsPtr(t.DueAt)})
	}
	if out.Names.Users, err = h.svc.UserNames(r.Context(), ids); err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// ---- affected resources ----

func (h *handler) addAffected(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int   `json:"expectedVersion"`
		Type            string `json:"type"`
		ID              string `json:"id"`
	}
	if !decode(w, r, &b) {
		return
	}
	l, created, err := h.svc.AddAffected(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.Type, b.ID)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, map[string]any{"relationshipId": l.RelationshipID, "type": l.Type, "id": l.ID, "confidence": l.Confidence,
		"since": ts(l.Since), "created": created})
}

func (h *handler) removeAffected(w http.ResponseWriter, r *http.Request) {
	expected, ok := parseVersion(w, r.URL.Query().Get("expectedVersion"))
	if !ok {
		return
	}
	err := h.svc.RemoveAffected(r.Context(), caller(w, r), principal(r), r.PathValue("id"), expected, r.PathValue("type"), r.PathValue("resourceId"))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- lifecycle ----

func (h *handler) respond(w http.ResponseWriter, r *http.Request, out application.Change, err error) {
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toChange(out))
}

func (h *handler) submit(w http.ResponseWriter, r *http.Request) {
	var b versionBody
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.Submit(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion)
	h.respond(w, r, out, err)
}

func (h *handler) assess(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion        *int    `json:"expectedVersion"`
		Risk                   string  `json:"risk"`
		ApproverUserID         *string `json:"approverUserId"`
		ApproverTeamID         *string `json:"approverTeamId"`
		EmergencyJustification string  `json:"emergencyJustification"`
	}
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.Assess(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, application.Assessment{
		Risk: b.Risk, Approver: application.Approver{UserID: b.ApproverUserID, TeamID: b.ApproverTeamID}, EmergencyJustification: b.EmergencyJustification})
	h.respond(w, r, out, err)
}

func (h *handler) schedule(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int        `json:"expectedVersion"`
		Window          *windowBody `json:"window"`
	}
	if !decode(w, r, &b) {
		return
	}
	in := application.ScheduleInput{}
	if b.Window != nil {
		wd := b.Window.window()
		in.Window = &wd
	}
	out, err := h.svc.Schedule(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, in)
	h.respond(w, r, out, err)
}

func (h *handler) start(w http.ResponseWriter, r *http.Request) {
	var b versionBody
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.Start(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion)
	h.respond(w, r, out, err)
}

func (h *handler) complete(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int   `json:"expectedVersion"`
		Force           string `json:"force"`
	}
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.Complete(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.Force)
	h.respond(w, r, out, err)
}

func (h *handler) failChange(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int   `json:"expectedVersion"`
		Reason          string `json:"reason"`
		RollbackDone    bool   `json:"rollbackDone"`
	}
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.Fail(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.Reason, b.RollbackDone)
	h.respond(w, r, out, err)
}

func (h *handler) review(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int   `json:"expectedVersion"`
		OutcomeNote     string `json:"outcomeNote"`
	}
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.Review(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.OutcomeNote)
	h.respond(w, r, out, err)
}

func (h *handler) closeChange(w http.ResponseWriter, r *http.Request) {
	var b versionBody
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.Close(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion)
	h.respond(w, r, out, err)
}

func (h *handler) cancel(w http.ResponseWriter, r *http.Request) {
	var b versionBody
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.Cancel(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.Reason)
	h.respond(w, r, out, err)
}

func (h *handler) addTask(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int       `json:"expectedVersion"`
		Title           string     `json:"title"`
		Description     string     `json:"description"`
		DueAt           *time.Time `json:"dueAt"`
		AssignedUserID  *string    `json:"assignedUserId"`
		AssignedTeamID  *string    `json:"assignedTeamId"`
	}
	if !decode(w, r, &b) {
		return
	}
	id, err := h.svc.AddTask(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, application.NewTask{Title: b.Title,
		Description: b.Description, DueAt: b.DueAt, AssignedUserID: b.AssignedUserID, AssignedTeamID: b.AssignedTeamID})
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]string{"taskId": id})
}

// ---- history and impact ----

type transitionDTO struct {
	ID            string  `json:"id"`
	FromStatus    *string `json:"fromStatus"`
	ToStatus      string  `json:"toStatus"`
	Operation     string  `json:"operation"`
	Reason        *string `json:"reason"`
	ActorUserID   *string `json:"actorUserId"`
	ActorSystem   *string `json:"actorSystem"`
	CorrelationID string  `json:"correlationId"`
	CreatedAt     string  `json:"createdAt"`
}

func (h *handler) transitions(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	res, err := h.svc.Transitions(r.Context(), principal(r), r.PathValue("id"), page)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	items := make([]transitionDTO, 0, len(res.Items))
	var ids []string
	for _, t := range res.Items {
		items = append(items, transitionDTO{ID: t.ID, FromStatus: t.FromStatus, ToStatus: t.ToStatus, Operation: t.Operation, Reason: t.Reason,
			ActorUserID: t.ActorUserID, ActorSystem: t.ActorSystem, CorrelationID: t.CorrelationID, CreatedAt: ts(t.CreatedAt)})
		if t.ActorUserID != nil {
			ids = append(ids, *t.ActorUserID)
		}
	}
	names, err := h.svc.UserNames(r.Context(), ids)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "nextCursor": res.NextCursor, "names": userNamesDTO{Users: names}})
}

type impactNodeDTO struct {
	Type        string  `json:"type"`
	ID          string  `json:"id"`
	Name        *string `json:"name"`
	Reference   *string `json:"reference"`
	Status      *string `json:"status"`
	Criticality *string `json:"criticality"`
	Missing     bool    `json:"missing,omitempty"`
	Hidden      bool    `json:"hidden,omitempty"`
	Depth       int     `json:"depth"`
	Confidence  string  `json:"confidence"`
}

type impactStartDTO struct {
	Type         string          `json:"type"`
	ID           string          `json:"id"`
	Name         *string         `json:"name"`
	Reference    *string         `json:"reference"`
	Nodes        []impactNodeDTO `json:"nodes"`
	Truncated    bool            `json:"truncated"`
	DepthLimited bool            `json:"depthLimited"`
	NodeLimited  bool            `json:"nodeLimited"`
}

func (h *handler) impact(w http.ResponseWriter, r *http.Request) {
	depth := 0
	if raw := r.URL.Query().Get("depth"); raw != "" {
		n := 0
		for _, ch := range raw {
			if ch < '0' || ch > '9' || n > 100 {
				httpx.WriteError(w, http.StatusBadRequest, "changes.invalid_request", "depth must be a number between 1 and 6.")
				return
			}
			n = n*10 + int(ch-'0')
		}
		depth = n
	}
	res, err := h.svc.Impact(r.Context(), principal(r), r.PathValue("id"), depth)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	starts := make([]impactStartDTO, 0, len(res.Starts))
	for _, s := range res.Starts {
		d := impactStartDTO{Type: s.Type, ID: s.ID, Name: s.Name, Reference: s.Reference, Truncated: s.Truncated, DepthLimited: s.DepthLimited,
			NodeLimited: s.NodeLimited, Nodes: make([]impactNodeDTO, 0, len(s.Nodes))}
		for _, n := range s.Nodes {
			d.Nodes = append(d.Nodes, impactNodeDTO{Type: n.Type, ID: n.ID, Name: n.Name, Reference: n.Reference, Status: n.Status,
				Criticality: n.Criticality, Missing: n.Missing, Hidden: n.Hidden, Depth: n.Depth, Confidence: n.Confidence})
		}
		starts = append(starts, d)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"starts": starts, "skipped": res.Skipped, "truncated": res.Truncated})
}

type lookupHitDTO struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Reference string `json:"reference,omitempty"`
	Name      string `json:"name"`
	Detail    string `json:"detail,omitempty"`
}

// affectedLookup finds Services, Virtual Machines and Assets for the affected-resource picker of the change wizard.
func (h *handler) affectedLookup(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 0
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			httpx.WriteError(w, http.StatusBadRequest, "changes.invalid_limit", "The limit must be a positive integer.")
			return
		}
		limit = n
	}
	hits, err := h.svc.LookupAffected(r.Context(), principal(r), q.Get("type"), q.Get("q"), limit)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	out := struct {
		Items []lookupHitDTO `json:"items"`
	}{Items: make([]lookupHitDTO, 0, len(hits))}
	for _, x := range hits {
		out.Items = append(out.Items, lookupHitDTO{Type: x.Type, ID: x.ID, Reference: x.Reference, Name: x.Name, Detail: x.Detail})
	}
	httpx.JSON(w, http.StatusOK, out)
}
