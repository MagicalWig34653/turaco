package transport

import (
	"net/http"
	"strconv"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

func registerRouting(route func(string, http.HandlerFunc), h *handler) {
	route("GET /api/v1/service-desk/routing-rules", h.listRules)
	route("POST /api/v1/service-desk/routing-rules", h.createRule)
	route("PUT /api/v1/service-desk/routing-rules/{id}", h.updateRule)
	route("DELETE /api/v1/service-desk/routing-rules/{id}", h.deleteRule)
	route("POST /api/v1/service-desk/routing-rules/reorder", h.reorderRules)
	route("POST /api/v1/service-desk/routing-rules/test", h.testRouting)
}

type ruleDTO struct {
	ID            string    `json:"id"`
	QueueID       string    `json:"queueId"`
	Position      int       `json:"position"`
	Name          string    `json:"name"`
	Keywords      []string  `json:"keywords"`
	Impact        string    `json:"impact,omitempty"`
	PatientImpact *bool     `json:"patientImpact,omitempty"`
	Enabled       bool      `json:"enabled"`
	Version       int       `json:"version"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

func toRule(r application.RoutingRule) ruleDTO {
	kw := r.Keywords
	if kw == nil {
		kw = []string{}
	}
	return ruleDTO{ID: r.ID, QueueID: r.QueueID, Position: r.Position, Name: r.Name, Keywords: kw, Impact: r.Impact, PatientImpact: r.PatientImpact,
		Enabled: r.Enabled, Version: r.Version, UpdatedAt: r.UpdatedAt}
}

type ruleBody struct {
	ExpectedVersion *int     `json:"expectedVersion"`
	QueueID         string   `json:"queueId"`
	Name            string   `json:"name"`
	Keywords        []string `json:"keywords"`
	Impact          string   `json:"impact"`
	PatientImpact   *bool    `json:"patientImpact"`
	Enabled         bool     `json:"enabled"`
}

func (b ruleBody) input() application.RoutingInput {
	return application.RoutingInput{QueueID: b.QueueID, Name: b.Name, Keywords: b.Keywords, Impact: b.Impact, PatientImpact: b.PatientImpact, Enabled: b.Enabled}
}

func rulesResponse(rules []application.RoutingRule) map[string]any {
	items := make([]ruleDTO, len(rules))
	for i, r := range rules {
		items[i] = toRule(r)
	}
	return map[string]any{"items": items}
}

func (h *handler) listRules(w http.ResponseWriter, r *http.Request) {
	rules, err := h.svc.ListRoutingRules(r.Context(), principal(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, rulesResponse(rules))
}

func (h *handler) createRule(w http.ResponseWriter, r *http.Request) {
	var b ruleBody
	if !decode(w, r, &b) {
		return
	}
	rule, err := h.svc.CreateRoutingRule(r.Context(), caller(w, r), principal(r), b.input())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toRule(rule))
}

func (h *handler) updateRule(w http.ResponseWriter, r *http.Request) {
	var b ruleBody
	if !decode(w, r, &b) {
		return
	}
	rule, err := h.svc.UpdateRoutingRule(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.input())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toRule(rule))
}

func (h *handler) deleteRule(w http.ResponseWriter, r *http.Request) {
	var expected *int
	if v := r.URL.Query().Get("expectedVersion"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "tickets.invalid_request", "expectedVersion must be a number.")
			return
		}
		expected = &n
	}
	if err := h.svc.DeleteRoutingRule(r.Context(), caller(w, r), principal(r), r.PathValue("id"), expected); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) reorderRules(w http.ResponseWriter, r *http.Request) {
	var b struct {
		IDs []string `json:"ids"`
	}
	if !decode(w, r, &b) {
		return
	}
	rules, err := h.svc.ReorderRoutingRules(r.Context(), caller(w, r), principal(r), b.IDs)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, rulesResponse(rules))
}

func (h *handler) testRouting(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Title         string `json:"title"`
		Description   string `json:"description"`
		Impact        string `json:"impact"`
		PatientImpact bool   `json:"patientImpact"`
	}
	if !decode(w, r, &b) {
		return
	}
	res, err := h.svc.TestRouting(r.Context(), principal(r), application.RoutingSample{Title: b.Title, Description: b.Description, Impact: b.Impact, PatientImpact: b.PatientImpact})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := map[string]any{"queue": map[string]any{"id": res.Queue.ID, "key": res.Queue.Key, "name": res.Queue.Name}, "defaultUsed": res.DefaultUsed, "rule": nil}
	if res.Rule != nil {
		out["rule"] = toRule(*res.Rule)
	}
	httpx.JSON(w, http.StatusOK, out)
}
