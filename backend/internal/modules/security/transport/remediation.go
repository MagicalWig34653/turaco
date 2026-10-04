package transport

import (
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
	"net/http"
	"time"
)

type taskBody struct {
	ExpectedVersion *int       `json:"expectedVersion"`
	AssignedUserID  *string    `json:"assignedUserId"`
	AssignedTeamID  *string    `json:"assignedTeamId"`
	DueAt           *time.Time `json:"dueAt"`
}

func (h *handler) createTask(w http.ResponseWriter, r *http.Request, kind string) {
	var b taskBody
	if !decode(w, r, &b, maxBody) {
		return
	}
	id, err := h.svc.CreateRemediationTask(r.Context(), caller(w, r), principal(r), kind, r.PathValue("id"), b.ExpectedVersion, b.AssignedUserID, b.AssignedTeamID, b.DueAt)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, 201, map[string]any{"taskId": id})
}
func (h *handler) createAdvisoryTask(w http.ResponseWriter, r *http.Request) {
	h.createTask(w, r, "advisory")
}
func (h *handler) createFindingTask(w http.ResponseWriter, r *http.Request) {
	h.createTask(w, r, "finding")
}
func (h *handler) tasks(w http.ResponseWriter, r *http.Request, kind string) {
	v, err := h.svc.RemediationTasks(r.Context(), principal(r), kind, r.PathValue("id"))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, 200, map[string]any{"items": v})
}
func (h *handler) advisoryTasks(w http.ResponseWriter, r *http.Request) { h.tasks(w, r, "advisory") }
func (h *handler) findingTasks(w http.ResponseWriter, r *http.Request)  { h.tasks(w, r, "finding") }
func (h *handler) advisoryChanges(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.LinkedChanges(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, 200, map[string]any{"items": v})
}
func (h *handler) linkChange(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int   `json:"expectedVersion"`
		ChangeID        string `json:"changeId"`
	}
	if !decode(w, r, &b, maxBody) {
		return
	}
	v, err := h.svc.LinkChange(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ChangeID, b.ExpectedVersion)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, 200, v)
}
func (h *handler) unlinkChange(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int `json:"expectedVersion"`
	}
	if !decode(w, r, &b, maxBody) {
		return
	}
	if err := h.svc.UnlinkChange(r.Context(), caller(w, r), principal(r), r.PathValue("id"), r.PathValue("changeId"), b.ExpectedVersion); err != nil {
		h.writeErr(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (h *handler) progress(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.Progress(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, 200, v)
}
func (h *handler) overview(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.Overview(r.Context(), principal(r))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, 200, v)
}
