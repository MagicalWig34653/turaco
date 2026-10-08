// Package transport exposes the Turaco AI HTTP API under /api/v1/ai. Responses are no-store. Authorization is
// decided in the service from the principal built from the HTTP session (never from the request body); the
// routes only require a signed-in User. With AI_ENABLED off only GET /ai/status is mounted. The API accepts no
// roles or messages from the browser other than the new plain user text (A12).
package transport

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/ai"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

const maxBody = 32 << 10

type handler struct {
	svc    *ai.Service
	logger *slog.Logger
}

// Register mounts the AI routes.
func Register(mux *http.ServeMux, svc *ai.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &handler{svc: svc, logger: logger}
	signedIn := authorization.RequireAuthenticated(auth)
	route := func(pattern string, fn http.HandlerFunc) { mux.Handle(pattern, httpx.NoStore(signedIn(fn))) }
	route("GET /api/v1/ai/status", h.status)
	if !svc.ModuleEnabled() {
		return
	}
	route("POST /api/v1/ai/conversations/messages", h.message)
	route("POST /api/v1/ai/conversations/transcript", h.transcript)
	route("POST /api/v1/ai/conversations/scope", h.consent)
	route("POST /api/v1/ai/conversations/end", h.end)
	route("GET /api/v1/ai/settings", h.getSettings)
	route("PUT /api/v1/ai/settings", h.putSettings)
	route("GET /api/v1/ai/providers", h.listProviders)
	route("POST /api/v1/ai/providers", h.createProvider)
	route("PUT /api/v1/ai/providers/{id}", h.updateProvider)
	route("POST /api/v1/ai/providers/{id}/test", h.testProvider)
	route("GET /api/v1/ai/usage", h.usage)
}

func caller(w http.ResponseWriter, r *http.Request) ai.Caller {
	p, _ := authorization.PrincipalFrom(r.Context())
	return ai.Caller{UserID: p.UserID, TenantID: p.TenantID, SessionID: p.SessionID, CorrelationID: httpx.RequestID(w), Permissions: p.Permissions}
}

func (h *handler) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var inv *ai.InvalidInputError
	switch {
	case errors.As(err, &inv):
		httpx.WriteError(w, http.StatusBadRequest, "ai.invalid_request", inv.Message)
	case errors.Is(err, ai.ErrDisabled):
		httpx.WriteError(w, http.StatusNotFound, "ai.disabled", "The assistant is not enabled.")
	case errors.Is(err, ai.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "ai.not_permitted", "You do not have permission to perform this action.")
	case errors.Is(err, ai.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "ai.not_found", "The requested resource was not found.")
	case errors.Is(err, ai.ErrVersionConflict):
		httpx.WriteError(w, http.StatusConflict, "ai.version_conflict", "The record was changed by someone else; reload and try again.")
	case errors.Is(err, ai.ErrRateLimited):
		httpx.WriteError(w, http.StatusTooManyRequests, "ai.rate_limited", "Too many assistant requests; try again later.")
	case errors.Is(err, ai.ErrBudgetExceeded):
		httpx.WriteError(w, http.StatusTooManyRequests, "ai.budget_exceeded", "The assistant budget is used up for today.")
	case errors.Is(err, ai.ErrProviderUnavailable):
		httpx.WriteError(w, http.StatusServiceUnavailable, "ai.provider_unavailable", "The AI provider is not available.")
	case errors.Is(err, ai.ErrProviderChanged):
		httpx.WriteError(w, http.StatusConflict, "ai.provider_changed", "The AI provider changed; start a new conversation.")
	case errors.Is(err, ai.ErrTurnInProgress):
		httpx.WriteError(w, http.StatusConflict, "ai.turn_in_progress", "The assistant is still answering the previous message.")
	case errors.Is(err, ai.ErrContextTooLarge):
		httpx.WriteError(w, http.StatusUnprocessableEntity, "ai.conversation_too_large", "The conversation is too long; start a new one.")
	default:
		h.logger.ErrorContext(r.Context(), "ai request failed", "request_id", httpx.RequestID(w), "method", r.Method, "path", r.URL.Path, "error", err)
		httpx.WriteError(w, http.StatusInternalServerError, "platform.internal_error", "An internal error occurred.")
	}
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := httpx.DecodeJSON(w, r, dst, maxBody); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "ai.invalid_request", "The request body is not valid JSON for this operation.")
		return false
	}
	return true
}

type usageDTO struct {
	Day                      string `json:"day"`
	RequestsToday            int    `json:"requestsToday"`
	RequestsThisHour         int    `json:"requestsThisHour"`
	RequestsTodayRemaining   int    `json:"requestsTodayRemaining"`
	RequestsHourRemaining    int    `json:"requestsHourRemaining"`
	TokensTodayRemaining     int64  `json:"tokensTodayRemaining"`
	InstallationTokensRemain int64  `json:"installationTokensRemaining"`
}

func toUsage(u ai.UsageSnapshot) usageDTO {
	return usageDTO{Day: u.Day, RequestsToday: u.UserRequestsToday, RequestsThisHour: u.UserRequestsThisHour,
		RequestsTodayRemaining: u.UserRequestsTodayLeft, RequestsHourRemaining: u.UserRequestsHourLeft,
		TokensTodayRemaining: u.UserTokensLeft, InstallationTokensRemain: u.InstallationTokenLeft}
}

func (h *handler) status(w http.ResponseWriter, r *http.Request) {
	c := caller(w, r)
	perms := map[string]bool{"use": c.Has(ai.PermUse), "settingsView": c.Has(ai.PermSettingsView) || c.Has(ai.PermSettingsManage),
		"settingsManage": c.Has(ai.PermSettingsManage), "usageView": c.Has(ai.PermUsageView)}
	out := map[string]any{"enabled": false, "permissions": perms, "provider": nil, "tools": []string{}, "usage": nil}
	if h.svc.ModuleEnabled() && c.Has(ai.PermUse) {
		st, err := h.svc.Status(r.Context(), c)
		if err != nil {
			h.writeErr(w, r, err)
			return
		}
		out["enabled"] = st.Enabled
		if st.Enabled {
			classes := make([]string, 0, len(st.Provider.AllowedDataClasses))
			for _, cl := range st.Provider.AllowedDataClasses {
				classes = append(classes, string(cl))
			}
			out["provider"] = map[string]any{"displayName": st.Provider.DisplayName, "local": st.Provider.Local, "model": st.Provider.Model, "allowedDataClasses": classes}
			out["tools"] = append([]string{}, st.Tools...)
			out["conversationTtlMinutes"] = st.ConversationTTLMin
			if st.Usage != nil {
				out["usage"] = toUsage(*st.Usage)
			}
		}
	}
	httpx.JSON(w, http.StatusOK, out)
}

type refDTO struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func (h *handler) message(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ConversationID string   `json:"conversationId"`
		Text           string   `json:"text"`
		Context        []refDTO `json:"context"`
	}
	if !decode(w, r, &b) {
		return
	}
	in := ai.TurnInput{ConversationID: b.ConversationID, Text: b.Text}
	for _, c := range b.Context {
		in.Context = append(in.Context, ai.ResourceRef{Type: c.Type, ID: c.ID})
	}
	res, err := h.svc.Turn(r.Context(), caller(w, r), in)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	used := make([]map[string]any, 0, len(res.ToolsUsed))
	for _, u := range res.ToolsUsed {
		var target any
		if u.Target != nil {
			target = refDTO{Type: u.Target.Type, ID: u.Target.ID}
		}
		used = append(used, map[string]any{"tool": u.Tool, "target": target, "itemCount": u.ItemCount, "outcome": u.Outcome})
	}
	reqs := make([]map[string]any, 0, len(res.ScopeRequests))
	for _, s := range res.ScopeRequests {
		reqs = append(reqs, map[string]any{"resourceType": s.ResourceType, "resourceId": s.ResourceID, "tool": s.Tool})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"conversationId": res.ConversationID, "answer": res.Answer, "stopReason": res.StopReason,
		"toolsUsed": used, "scopeRequests": reqs, "tokens": map[string]int{"in": res.TokensIn, "out": res.TokensOut}, "usage": toUsage(res.Usage)})
}

func (h *handler) transcript(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ConversationID string `json:"conversationId"`
	}
	if !decode(w, r, &b) {
		return
	}
	msgs, err := h.svc.Transcript(r.Context(), caller(w, r), b.ConversationID)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	items := make([]map[string]string, 0, len(msgs))
	for _, m := range msgs {
		items = append(items, map[string]string{"role": m.Role, "content": m.Content})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handler) consent(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ConversationID string `json:"conversationId"`
		ResourceType   string `json:"resourceType"`
		ResourceID     string `json:"resourceId"`
	}
	if !decode(w, r, &b) {
		return
	}
	if err := h.svc.Consent(r.Context(), caller(w, r), b.ConversationID, ai.ResourceRef{Type: b.ResourceType, ID: b.ResourceID}); err != nil {
		h.writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) end(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ConversationID string `json:"conversationId"`
	}
	if !decode(w, r, &b) {
		return
	}
	if err := h.svc.EndConversation(r.Context(), caller(w, r), b.ConversationID); err != nil {
		h.writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type settingsDTO struct {
	Enabled                  bool    `json:"enabled"`
	RetainConversations      bool    `json:"retainConversations"`
	RetentionDays            int     `json:"retentionDays"`
	UserRequestsPerHour      int     `json:"userRequestsPerHour"`
	UserRequestsPerDay       int     `json:"userRequestsPerDay"`
	UserTokensPerDay         int     `json:"userTokensPerDay"`
	InstallationTokensPerDay int     `json:"installationTokensPerDay"`
	MaxOutputTokens          int     `json:"maxOutputTokens"`
	MaxToolIterations        int     `json:"maxToolIterations"`
	UpdatedBy                *string `json:"updatedBy"`
	UpdatedAt                string  `json:"updatedAt"`
	Version                  int     `json:"version"`
}

func toSettings(s ai.Settings) settingsDTO {
	return settingsDTO{Enabled: s.Enabled, RetainConversations: s.RetainConversations, RetentionDays: s.RetentionDays,
		UserRequestsPerHour: s.UserRequestsPerHour, UserRequestsPerDay: s.UserRequestsPerDay, UserTokensPerDay: s.UserTokensPerDay,
		InstallationTokensPerDay: s.InstallationTokensPerDay, MaxOutputTokens: s.MaxOutputTokens, MaxToolIterations: s.MaxToolIterations,
		UpdatedBy: s.UpdatedBy, UpdatedAt: s.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"), Version: s.Version}
}

func (h *handler) getSettings(w http.ResponseWriter, r *http.Request) {
	s, err := h.svc.GetSettings(r.Context(), caller(w, r))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toSettings(s))
}

func (h *handler) putSettings(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Enabled                  bool `json:"enabled"`
		RetainConversations      bool `json:"retainConversations"`
		RetentionDays            int  `json:"retentionDays"`
		UserRequestsPerHour      int  `json:"userRequestsPerHour"`
		UserRequestsPerDay       int  `json:"userRequestsPerDay"`
		UserTokensPerDay         int  `json:"userTokensPerDay"`
		InstallationTokensPerDay int  `json:"installationTokensPerDay"`
		MaxOutputTokens          int  `json:"maxOutputTokens"`
		MaxToolIterations        int  `json:"maxToolIterations"`
		ExpectedVersion          *int `json:"expectedVersion"`
	}
	if !decode(w, r, &b) {
		return
	}
	s, err := h.svc.UpdateSettings(r.Context(), caller(w, r), ai.SettingsInput{Enabled: b.Enabled, RetainConversations: b.RetainConversations,
		RetentionDays: b.RetentionDays, UserRequestsPerHour: b.UserRequestsPerHour, UserRequestsPerDay: b.UserRequestsPerDay,
		UserTokensPerDay: b.UserTokensPerDay, InstallationTokensPerDay: b.InstallationTokensPerDay, MaxOutputTokens: b.MaxOutputTokens,
		MaxToolIterations: b.MaxToolIterations}, b.ExpectedVersion)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toSettings(s))
}

type providerDTO struct {
	ID                  string   `json:"id"`
	Kind                string   `json:"kind"`
	DisplayName         string   `json:"displayName"`
	EndpointURL         string   `json:"endpointUrl"`
	Model               string   `json:"model"`
	Local               bool     `json:"local"`
	AllowedDataClasses  []string `json:"allowedDataClasses"`
	DPARecordedOn       *string  `json:"dpaRecordedOn"`
	NoTrainingConfirmed bool     `json:"noTrainingConfirmed"`
	Region              string   `json:"region"`
	SecretRef           *string  `json:"secretRef"`
	Enabled             bool     `json:"enabled"`
	PriceInPerMTok      float64  `json:"priceInPerMTok"`
	PriceOutPerMTok     float64  `json:"priceOutPerMTok"`
	Version             int      `json:"version"`
	CreatedAt           string   `json:"createdAt"`
	UpdatedAt           string   `json:"updatedAt"`
}

func toProvider(p ai.ProviderRecord) providerDTO {
	classes := make([]string, 0, len(p.AllowedDataClasses))
	for _, c := range p.AllowedDataClasses {
		classes = append(classes, string(c))
	}
	return providerDTO{ID: p.ID, Kind: p.Kind, DisplayName: p.DisplayName, EndpointURL: p.EndpointURL, Model: p.Model, Local: p.Local,
		AllowedDataClasses: classes, DPARecordedOn: p.DPARecordedOn, NoTrainingConfirmed: p.NoTrainingConfirm, Region: p.Region, SecretRef: p.SecretRef,
		Enabled: p.Enabled, PriceInPerMTok: p.PriceInPerMTok, PriceOutPerMTok: p.PriceOutPerMTok, Version: p.Version, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}

type providerBody struct {
	Kind                string    `json:"kind"`
	DisplayName         string    `json:"displayName"`
	EndpointURL         string    `json:"endpointUrl"`
	Model               string    `json:"model"`
	Local               bool      `json:"local"`
	AllowedDataClasses  *[]string `json:"allowedDataClasses"`
	DPARecordedOn       string    `json:"dpaRecordedOn"`
	NoTrainingConfirmed bool      `json:"noTrainingConfirmed"`
	Region              string    `json:"region"`
	SecretRef           string    `json:"secretRef"`
	Enabled             bool      `json:"enabled"`
	PriceInPerMTok      float64   `json:"priceInPerMTok"`
	PriceOutPerMTok     float64   `json:"priceOutPerMTok"`
	ExpectedVersion     *int      `json:"expectedVersion"`
}

func (b providerBody) input() ai.ProviderInput {
	in := ai.ProviderInput{Kind: b.Kind, DisplayName: b.DisplayName, EndpointURL: b.EndpointURL, Model: b.Model, Local: b.Local,
		DPARecordedOn: b.DPARecordedOn, NoTrainingConfirmed: b.NoTrainingConfirmed, Region: b.Region, SecretRef: b.SecretRef, Enabled: b.Enabled,
		PriceInPerMTok: b.PriceInPerMTok, PriceOutPerMTok: b.PriceOutPerMTok}
	if b.AllowedDataClasses != nil {
		in.AllowedDataClasses = []ai.DataClass{}
		for _, c := range *b.AllowedDataClasses {
			in.AllowedDataClasses = append(in.AllowedDataClasses, ai.DataClass(c))
		}
	}
	return in
}

func (h *handler) listProviders(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.ListProviders(r.Context(), caller(w, r))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	items := make([]providerDTO, 0, len(list))
	for _, p := range list {
		items = append(items, toProvider(p))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handler) createProvider(w http.ResponseWriter, r *http.Request) {
	var b providerBody
	if !decode(w, r, &b) {
		return
	}
	p, err := h.svc.CreateProvider(r.Context(), caller(w, r), b.input())
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toProvider(p))
}

func (h *handler) updateProvider(w http.ResponseWriter, r *http.Request) {
	var b providerBody
	if !decode(w, r, &b) {
		return
	}
	p, err := h.svc.UpdateProvider(r.Context(), caller(w, r), r.PathValue("id"), b.ExpectedVersion, b.input())
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toProvider(p))
}

func (h *handler) testProvider(w http.ResponseWriter, r *http.Request) {
	res, err := h.svc.TestProvider(r.Context(), caller(w, r), r.PathValue("id"))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": res.OK, "code": res.Code, "durationMs": res.DurationMs})
}

func (h *handler) usage(w http.ResponseWriter, r *http.Request) {
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "ai.invalid_request", "days must be an integer")
			return
		}
		days = n
	}
	list, err := h.svc.Usage(r.Context(), caller(w, r), days)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	items := make([]map[string]any, 0, len(list))
	for _, u := range list {
		items = append(items, map[string]any{"day": u.Day, "users": u.Users, "requests": u.Requests, "tokensIn": u.TokensIn,
			"tokensOut": u.TokensOut, "estimatedCostMicro": u.CostMicro})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}
