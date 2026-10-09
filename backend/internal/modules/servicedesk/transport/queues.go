package transport

import (
	"context"
	"net/http"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

func registerQueues(route func(string, http.HandlerFunc), h *handler) {
	route("GET /api/v1/service-desk/queues", h.listQueues)
	route("POST /api/v1/service-desk/queues", h.createQueue)
	route("GET /api/v1/service-desk/queues/{id}", h.getQueue)
	route("PATCH /api/v1/service-desk/queues/{id}", h.updateQueue)
	route("POST /api/v1/service-desk/queues/{id}/archive", h.queueLifecycle(h.svc.ArchiveQueue))
	route("POST /api/v1/service-desk/queues/{id}/restore", h.queueLifecycle(h.svc.RestoreQueue))
	route("POST /api/v1/service-desk/queues/{id}/make-default", h.queueLifecycle(h.svc.MakeDefaultQueue))
	route("PUT /api/v1/service-desk/queues/{id}/grants", h.replaceGrants)
}

type grantDTO struct {
	SubjectType string    `json:"subjectType"`
	SubjectID   string    `json:"subjectId"`
	Level       string    `json:"level"`
	GrantedBy   string    `json:"grantedBy,omitempty"`
	GrantedAt   time.Time `json:"grantedAt"`
}

type queueDTO struct {
	ID              string     `json:"id"`
	Key             string     `json:"key"`
	Prefix          string     `json:"prefix"`
	Name            string     `json:"name"`
	Description     string     `json:"description"`
	PublicLabel     string     `json:"publicLabel,omitempty"`
	Status          string     `json:"status"`
	Visibility      string     `json:"visibility"`
	RoutingMode     string     `json:"routingMode,omitempty"`
	DefaultPriority string     `json:"defaultPriority,omitempty"`
	DefaultTeamID   *string    `json:"defaultTeamId,omitempty"`
	IsDefault       bool       `json:"isDefault"`
	Version         int        `json:"version"`
	Level           string     `json:"level"`
	CanCreate       bool       `json:"canCreate"`
	Grants          []grantDTO `json:"grants,omitempty"`
	ArchivedAt      *string    `json:"archivedAt,omitempty"`
}

func toQueue(v application.QueueView) queueDTO {
	q := v.Queue
	out := queueDTO{ID: q.ID, Key: q.Key, Prefix: q.Prefix, Name: q.Name, Description: q.Description, PublicLabel: q.PublicLabel, Status: q.Status,
		Visibility: q.Visibility, RoutingMode: q.RoutingMode, DefaultPriority: q.DefaultPriority, DefaultTeamID: q.DefaultTeamID,
		IsDefault: q.DefaultIntake, Version: q.Version, Level: v.Level, CanCreate: v.CanCreate, ArchivedAt: tsPtr(q.ArchivedAt)}
	for _, g := range v.Grants {
		out.Grants = append(out.Grants, grantDTO{SubjectType: g.SubjectType, SubjectID: g.SubjectID, Level: g.Level, GrantedBy: g.GrantedBy, GrantedAt: g.GrantedAt})
	}
	return out
}

// queueByID wraps a Queue the administration returned.
func (h *handler) viewOf(w http.ResponseWriter, r *http.Request, q application.Queue) {
	v, err := h.svc.GetQueue(r.Context(), principal(r), q.ID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toQueue(v))
}

func (h *handler) listQueues(w http.ResponseWriter, r *http.Request) {
	forCreate := false
	switch r.URL.Query().Get("for") {
	case "":
	case "create":
		forCreate = true
	default:
		httpx.WriteError(w, http.StatusBadRequest, "tickets.invalid_request", "for must be create.")
		return
	}
	items, err := h.svc.ListQueues(r.Context(), principal(r), forCreate)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]queueDTO, 0, len(items))
	for _, v := range items {
		out = append(out, toQueue(v))
	}
	httpx.JSON(w, http.StatusOK, struct {
		Items []queueDTO `json:"items"`
	}{out})
}

func (h *handler) getQueue(w http.ResponseWriter, r *http.Request) {
	v, err := h.svc.GetQueue(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toQueue(v))
}

func (h *handler) createQueue(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Key             string  `json:"key"`
		Prefix          string  `json:"prefix"`
		Name            string  `json:"name"`
		Description     string  `json:"description"`
		PublicLabel     string  `json:"publicLabel"`
		Visibility      string  `json:"visibility"`
		RoutingMode     string  `json:"routingMode"`
		DefaultPriority string  `json:"defaultPriority"`
		DefaultTeamID   *string `json:"defaultTeamId"`
		NumberPadding   int     `json:"numberPadding"`
	}
	if !decode(w, r, &b) {
		return
	}
	q, err := h.svc.CreateQueue(r.Context(), caller(w, r), principal(r), application.QueueInput{Key: b.Key, Prefix: b.Prefix, Name: b.Name,
		Description: b.Description, PublicLabel: b.PublicLabel, Visibility: b.Visibility, RoutingMode: b.RoutingMode, DefaultPriority: b.DefaultPriority,
		DefaultTeamID: b.DefaultTeamID, NumberPadding: b.NumberPadding})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	v, err := h.svc.GetQueue(r.Context(), principal(r), q.ID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toQueue(v))
}

func (h *handler) updateQueue(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int    `json:"expectedVersion"`
		Name            *string `json:"name"`
		Description     *string `json:"description"`
		PublicLabel     *string `json:"publicLabel"`
		Visibility      *string `json:"visibility"`
		RoutingMode     *string `json:"routingMode"`
		DefaultPriority *string `json:"defaultPriority"`
		DefaultTeamID   *string `json:"defaultTeamId"`
	}
	if !decode(w, r, &b) {
		return
	}
	q, err := h.svc.UpdateQueue(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, application.QueueUpdate{Name: b.Name,
		Description: b.Description, PublicLabel: b.PublicLabel, Visibility: b.Visibility, RoutingMode: b.RoutingMode, DefaultPriority: b.DefaultPriority,
		DefaultTeamID: b.DefaultTeamID})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.viewOf(w, r, q)
}

type queueOp func(ctx context.Context, c application.Caller, p application.Principal, id string, expected *int) (application.Queue, error)

func (h *handler) queueLifecycle(op queueOp) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			ExpectedVersion *int `json:"expectedVersion"`
		}
		if !decode(w, r, &b) {
			return
		}
		q, err := op(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		h.viewOf(w, r, q)
	}
}

func (h *handler) replaceGrants(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int `json:"expectedVersion"`
		Grants          []struct {
			SubjectType string `json:"subjectType"`
			SubjectID   string `json:"subjectId"`
			Level       string `json:"level"`
		} `json:"grants"`
	}
	if !decode(w, r, &b) {
		return
	}
	in := make([]application.GrantInput, 0, len(b.Grants))
	for _, g := range b.Grants {
		in = append(in, application.GrantInput{SubjectType: g.SubjectType, SubjectID: g.SubjectID, Level: g.Level})
	}
	v, err := h.svc.ReplaceGrants(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toQueue(v))
}
