package transport

import (
	"context"
	"net/http"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/planning/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

type initiativeDTO struct {
	ID                string   `json:"id"`
	Reference         string   `json:"reference"`
	Title             string   `json:"title"`
	Goal              *string  `json:"goal"`
	OwnerID           string   `json:"ownerId"`
	Status            string   `json:"status"`
	StatusReason      *string  `json:"statusReason"`
	TargetDate        *string  `json:"targetDate"`
	ApprovalID        *string  `json:"approvalId"`
	ProposedBy        *string  `json:"proposedBy"`
	CreatedBy         string   `json:"createdBy"`
	ApprovedAt        *string  `json:"approvedAt"`
	ActivatedAt       *string  `json:"activatedAt"`
	ClosedAt          *string  `json:"closedAt"`
	AllowedOperations []string `json:"allowedOperations,omitempty"`
	Version           int      `json:"version"`
	CreatedAt         string   `json:"createdAt"`
	UpdatedAt         string   `json:"updatedAt"`
}

func toInitiative(i application.Initiative) initiativeDTO {
	return initiativeDTO{ID: i.ID, Reference: i.Reference, Title: i.Title, Goal: i.Goal, OwnerID: i.OwnerID, Status: i.Status,
		StatusReason: i.StatusReason, TargetDate: datePtr(i.TargetDate), ApprovalID: i.ApprovalID, ProposedBy: i.ProposedBy, CreatedBy: i.CreatedBy,
		ApprovedAt: tsPtr(i.ApprovedAt), ActivatedAt: tsPtr(i.ActivatedAt), ClosedAt: tsPtr(i.ClosedAt),
		Version: i.Version, CreatedAt: ts(i.CreatedAt), UpdatedAt: ts(i.UpdatedAt)}
}

type userNamesDTO struct {
	Users map[string]string `json:"users"`
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	from, ok := parseDate(w, "targetFrom", q.Get("targetFrom"))
	if !ok {
		return
	}
	to, ok := parseDate(w, "targetTo", q.Get("targetTo"))
	if !ok {
		return
	}
	res, err := h.svc.List(r.Context(), principal(r), application.Filter{Status: q.Get("status"), OwnerID: q.Get("owner"), Query: q.Get("q"),
		TargetFrom: from, TargetTo: to, Page: page})
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	items := make([]initiativeDTO, 0, len(res.Items))
	ids := []string{}
	for _, i := range res.Items {
		items = append(items, toInitiative(i))
		ids = append(ids, i.OwnerID)
	}
	names, err := h.svc.UserNames(r.Context(), ids)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "nextCursor": res.NextCursor, "names": userNamesDTO{Users: names}})
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Title       string `json:"title"`
		Goal        string `json:"goal"`
		OwnerUserID string `json:"ownerUserId"`
		TargetDate  string `json:"targetDate"`
	}
	if !decode(w, r, &b) {
		return
	}
	target, ok := parseDate(w, "targetDate", b.TargetDate)
	if !ok {
		return
	}
	out, err := h.svc.Create(r.Context(), caller(w, r), principal(r), application.NewInitiative{Title: b.Title, Goal: b.Goal,
		OwnerUserID: b.OwnerUserID, TargetDate: target})
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toInitiative(out))
}

func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int    `json:"expectedVersion"`
		Title           *string `json:"title"`
		Goal            *string `json:"goal"`
		OwnerUserID     *string `json:"ownerUserId"`
		// TargetDate is a YYYY-MM-DD date; an empty string clears it.
		TargetDate *string `json:"targetDate"`
	}
	if !decode(w, r, &b) {
		return
	}
	in := application.Details{Title: b.Title, Goal: b.Goal, OwnerUserID: b.OwnerUserID}
	if b.TargetDate != nil {
		if *b.TargetDate == "" {
			in.ClearTargetDate = true
		} else {
			t, ok := parseDate(w, "targetDate", *b.TargetDate)
			if !ok {
				return
			}
			in.TargetDate = t
		}
	}
	out, err := h.svc.UpdateDetails(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, in)
	h.respond(w, r, out, err)
}

func (h *handler) respond(w http.ResponseWriter, r *http.Request, out application.Initiative, err error) {
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toInitiative(out))
}

type lifecycleFn func(context.Context, application.Caller, application.Principal, string, *int) (application.Initiative, error)

func (h *handler) lifecycle(fn lifecycleFn) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b versionBody
		if !decode(w, r, &b) {
			return
		}
		out, err := fn(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion)
		h.respond(w, r, out, err)
	}
}

type reasonFn func(context.Context, application.Caller, application.Principal, string, *int, string) (application.Initiative, error)

func (h *handler) withReason(fn reasonFn) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b versionBody
		if !decode(w, r, &b) {
			return
		}
		out, err := fn(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.Reason)
		h.respond(w, r, out, err)
	}
}

func (h *handler) propose(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int    `json:"expectedVersion"`
		ApproverUserID  *string `json:"approverUserId"`
		ApproverTeamID  *string `json:"approverTeamId"`
	}
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.Propose(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion,
		application.Approver{UserID: b.ApproverUserID, TeamID: b.ApproverTeamID})
	h.respond(w, r, out, err)
}

// ---- detail ----

type itemDTO struct {
	RelationshipID string  `json:"relationshipId"`
	Type           string  `json:"type"`
	ID             string  `json:"id"`
	Reference      *string `json:"reference"`
	Title          *string `json:"title"`
	Status         *string `json:"status"`
	Since          string  `json:"since"`
	Missing        bool    `json:"missing,omitempty"`
	Hidden         bool    `json:"hidden,omitempty"`
}

type itemPageDTO struct {
	Items      []itemDTO `json:"items"`
	NextCursor string    `json:"nextCursor,omitempty"`
	Truncated  bool      `json:"truncated"`
}

func toItems(p application.ItemPage) itemPageDTO {
	out := itemPageDTO{Items: make([]itemDTO, 0, len(p.Items)), NextCursor: p.NextCursor, Truncated: p.NextCursor != ""}
	for _, it := range p.Items {
		out.Items = append(out.Items, itemDTO{RelationshipID: it.RelationshipID, Type: it.Type, ID: it.ID, Reference: it.Reference, Title: it.Title,
			Status: it.Status, Since: ts(it.Since), Missing: it.Missing, Hidden: it.Hidden})
	}
	return out
}

type milestoneDTO struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	DueDate   string  `json:"dueDate"`
	Position  int     `json:"position"`
	DoneAt    *string `json:"doneAt"`
	DoneBy    *string `json:"doneBy"`
	Version   int     `json:"version"`
	CreatedAt string  `json:"createdAt"`
	UpdatedAt string  `json:"updatedAt"`
}

func toMilestone(m application.Milestone) milestoneDTO {
	return milestoneDTO{ID: m.ID, Title: m.Title, DueDate: date(m.DueDate), Position: m.Position, DoneAt: tsPtr(m.DoneAt), DoneBy: m.DoneBy,
		Version: m.Version, CreatedAt: ts(m.CreatedAt), UpdatedAt: ts(m.UpdatedAt)}
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

type progressDTO struct {
	Items              int            `json:"items"`
	ItemsLimitExceeded bool           `json:"itemsLimitExceeded"`
	ChangesByStatus    map[string]int `json:"changesByStatus"`
	TasksByStatus      map[string]int `json:"tasksByStatus"`
	RequestsByStatus   map[string]int `json:"procurementRequestsByStatus"`
	Milestones         struct {
		Done    int `json:"done"`
		Total   int `json:"total"`
		Overdue int `json:"overdue"`
	} `json:"milestones"`
}

type detailDTO struct {
	initiativeDTO
	Items      itemPageDTO    `json:"items"`
	Milestones []milestoneDTO `json:"milestones"`
	Approvals  []approvalDTO  `json:"approvals"`
	Progress   progressDTO    `json:"progress"`
	Names      userNamesDTO   `json:"names"`
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	d, err := h.svc.Get(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	out := detailDTO{initiativeDTO: toInitiative(d.Initiative), Items: toItems(d.Items), Milestones: make([]milestoneDTO, 0, len(d.Milestones)),
		Approvals: make([]approvalDTO, 0, len(d.Approvals))}
	out.AllowedOperations = d.AllowedOps
	for _, m := range d.Milestones {
		out.Milestones = append(out.Milestones, toMilestone(m))
	}
	ids := []string{d.Initiative.OwnerID, d.Initiative.CreatedBy}
	for _, a := range d.Approvals {
		out.Approvals = append(out.Approvals, approvalDTO{ID: a.ID, StepIndex: a.StepIndex, Status: a.Status, ApproverUserID: a.ApproverUserID,
			ApproverTeamID: a.ApproverTeamID, DecidedByUserID: a.DecidedByUserID, DecidedAt: tsPtr(a.DecidedAt)})
		for _, u := range []*string{a.ApproverUserID, a.DecidedByUserID} {
			if u != nil {
				ids = append(ids, *u)
			}
		}
	}
	pr := d.Progress
	out.Progress = progressDTO{Items: pr.Items, ItemsLimitExceeded: pr.ItemsLimitExceeded, ChangesByStatus: pr.ChangesByStatus,
		TasksByStatus: pr.TasksByStatus, RequestsByStatus: pr.RequestsByStatus}
	out.Progress.Milestones.Done, out.Progress.Milestones.Total, out.Progress.Milestones.Overdue = pr.MilestonesDone, pr.MilestonesTotal, pr.MilestonesOverdue
	if out.Names.Users, err = h.svc.UserNames(r.Context(), ids); err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *handler) items(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	res, err := h.svc.Items(r.Context(), principal(r), r.PathValue("id"), page)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toItems(res))
}

func (h *handler) addItem(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int   `json:"expectedVersion"`
		Type            string `json:"type"`
		ID              string `json:"id"`
	}
	if !decode(w, r, &b) {
		return
	}
	l, created, err := h.svc.AddItem(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.Type, b.ID)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, map[string]any{"relationshipId": l.RelationshipID, "type": l.Type, "id": l.ID, "since": ts(l.Since), "created": created})
}

func (h *handler) removeItem(w http.ResponseWriter, r *http.Request) {
	expected, ok := parseVersion(w, r.URL.Query().Get("expectedVersion"))
	if !ok {
		return
	}
	if err := h.svc.RemoveItem(r.Context(), caller(w, r), principal(r), r.PathValue("id"), expected, r.PathValue("type"), r.PathValue("itemId")); err != nil {
		h.writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- milestones ----

func (h *handler) respondMilestone(w http.ResponseWriter, r *http.Request, status int, m application.Milestone, err error) {
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, status, toMilestone(m))
}

func (h *handler) addMilestone(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int   `json:"expectedVersion"`
		Title           string `json:"title"`
		DueDate         string `json:"dueDate"`
		Position        *int   `json:"position"`
	}
	if !decode(w, r, &b) {
		return
	}
	due, ok := parseDate(w, "dueDate", b.DueDate)
	if !ok {
		return
	}
	in := application.MilestoneInput{Title: b.Title, Position: b.Position}
	if due != nil {
		in.DueDate = *due
	}
	m, err := h.svc.AddMilestone(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, in)
	h.respondMilestone(w, r, http.StatusCreated, m, err)
}

func (h *handler) updateMilestone(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int    `json:"expectedVersion"`
		Title           *string `json:"title"`
		DueDate         string  `json:"dueDate"`
		Position        *int    `json:"position"`
	}
	if !decode(w, r, &b) {
		return
	}
	due, ok := parseDate(w, "dueDate", b.DueDate)
	if !ok {
		return
	}
	m, err := h.svc.UpdateMilestone(r.Context(), caller(w, r), principal(r), r.PathValue("id"), r.PathValue("milestoneId"), b.ExpectedVersion,
		application.MilestoneChange{Title: b.Title, DueDate: due, Position: b.Position})
	h.respondMilestone(w, r, http.StatusOK, m, err)
}

type milestoneFn func(context.Context, application.Caller, application.Principal, string, string, *int) (application.Milestone, error)

func (h *handler) milestoneOp(fn milestoneFn) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b versionBody
		if !decode(w, r, &b) {
			return
		}
		m, err := fn(r.Context(), caller(w, r), principal(r), r.PathValue("id"), r.PathValue("milestoneId"), b.ExpectedVersion)
		h.respondMilestone(w, r, http.StatusOK, m, err)
	}
}

func (h *handler) removeMilestone(w http.ResponseWriter, r *http.Request) {
	var b versionBody
	if !decode(w, r, &b) {
		return
	}
	m, err := h.svc.RemoveMilestone(r.Context(), caller(w, r), principal(r), r.PathValue("id"), r.PathValue("milestoneId"), b.ExpectedVersion, b.Reason)
	h.respondMilestone(w, r, http.StatusOK, m, err)
}

// ---- history ----

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

// ---- maintenance calendar ----

type calendarAffectedDTO struct {
	Type      string  `json:"type"`
	ID        string  `json:"id"`
	Name      *string `json:"name"`
	Reference *string `json:"reference"`
	Missing   bool    `json:"missing,omitempty"`
	Hidden    bool    `json:"hidden,omitempty"`
}

type initiativeRefDTO struct {
	ID        string `json:"id"`
	Reference string `json:"reference"`
	Title     string `json:"title"`
}

type calendarItemDTO struct {
	ChangeID    string                `json:"changeId"`
	Reference   string                `json:"reference"`
	Title       *string               `json:"title"`
	Kind        *string               `json:"kind"`
	Risk        *string               `json:"risk"`
	Status      string                `json:"status"`
	Proposed    bool                  `json:"proposed"`
	WindowStart string                `json:"windowStart"`
	WindowEnd   string                `json:"windowEnd"`
	Affected    []calendarAffectedDTO `json:"affected"`
	Initiatives []initiativeRefDTO    `json:"initiatives"`
}

func (h *handler) calendar(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, ok := parseTime(w, "from", q.Get("from"))
	if !ok {
		return
	}
	to, ok := parseTime(w, "to", q.Get("to"))
	if !ok {
		return
	}
	cal, err := h.svc.MaintenanceCalendar(r.Context(), principal(r), from, to)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	items := make([]calendarItemDTO, 0, len(cal.Items))
	for _, it := range cal.Items {
		d := calendarItemDTO{ChangeID: it.ChangeID, Reference: it.Reference, Title: it.Title, Kind: it.Kind, Risk: it.Risk, Status: it.Status, Proposed: it.Proposed,
			WindowStart: ts(it.WindowStart), WindowEnd: ts(it.WindowEnd), Affected: make([]calendarAffectedDTO, 0, len(it.Affected)),
			Initiatives: make([]initiativeRefDTO, 0, len(it.Initiatives))}
		for _, a := range it.Affected {
			d.Affected = append(d.Affected, calendarAffectedDTO{Type: a.Type, ID: a.ID, Name: a.Name, Reference: a.Reference, Missing: a.Missing, Hidden: a.Hidden})
		}
		for _, i := range it.Initiatives {
			d.Initiatives = append(d.Initiatives, initiativeRefDTO{ID: i.ID, Reference: i.Reference, Title: i.Title})
		}
		items = append(items, d)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"from": ts(cal.From), "to": ts(cal.To), "items": items, "truncated": cal.Truncated})
}
