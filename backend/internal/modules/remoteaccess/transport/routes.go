package transport

import (
	"context"
	"net/http"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/remoteaccess/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

type sessionDTO struct {
	ID          string  `json:"id"`
	Reference   string  `json:"reference"`
	DeviceID    string  `json:"deviceId"`
	TicketID    string  `json:"ticketId"`
	Provider    string  `json:"provider"`
	Mode        string  `json:"mode"`
	Status      string  `json:"status"`
	Reason      *string `json:"statusReason"`
	InitiatedBy string  `json:"initiatedBy"`
	ApprovalID  *string `json:"approvalId"`
	// Consent is separate from authorization and from the provider's records.
	Consent           string  `json:"consent"`
	ConsentRecordedBy *string `json:"consentRecordedBy"`
	ConsentRecordedAt *string `json:"consentRecordedAt"`
	MismatchReason    *string `json:"mismatchReason"`
	LaunchedAt        *string `json:"launchedAt"`
	ClosedAt          *string `json:"closedAt"`
	ExpiresAt         string  `json:"expiresAt"`
	Note              *string `json:"note"`
	// Observed* are provider-reported facts with source and observation time; null means unknown.
	ObservedConnectedAt *string `json:"observedConnectedAt"`
	ObservedEndedAt     *string `json:"observedEndedAt"`
	ObservedSource      *string `json:"observedSource"`
	ObservedAt          *string `json:"observedAt"`
	Version             int     `json:"version"`
	CreatedAt           string  `json:"createdAt"`
	UpdatedAt           string  `json:"updatedAt"`
}

func toSession(s application.Session) sessionDTO {
	return sessionDTO{ID: s.ID, Reference: s.Reference, DeviceID: s.DeviceID, TicketID: s.TicketID, Provider: s.Provider, Mode: s.Mode,
		Status: s.Status, Reason: s.StatusReason, InitiatedBy: s.InitiatedBy, ApprovalID: s.ApprovalID, Consent: s.Consent,
		ConsentRecordedBy: s.ConsentRecordedBy, ConsentRecordedAt: tsPtr(s.ConsentRecordedAt), MismatchReason: s.MismatchReason,
		LaunchedAt: tsPtr(s.LaunchedAt), ClosedAt: tsPtr(s.ClosedAt), ExpiresAt: ts(s.ExpiresAt), Note: s.Note,
		ObservedConnectedAt: tsPtr(s.ObservedConnectedAt), ObservedEndedAt: tsPtr(s.ObservedEndedAt), ObservedSource: s.ObservedSource,
		ObservedAt: tsPtr(s.ObservedAt), Version: s.Version, CreatedAt: ts(s.CreatedAt), UpdatedAt: ts(s.UpdatedAt)}
}

type transitionDTO struct {
	ID          string  `json:"id"`
	FromStatus  *string `json:"fromStatus"`
	ToStatus    string  `json:"toStatus"`
	Operation   string  `json:"operation"`
	Reason      *string `json:"reason"`
	ActorUserID *string `json:"actorUserId"`
	ActorSystem *string `json:"actorSystem"`
	CreatedAt   string  `json:"createdAt"`
}

func (h *handler) request(w http.ResponseWriter, r *http.Request) {
	var b struct {
		DeviceID       string  `json:"deviceId"`
		TicketID       string  `json:"ticketId"`
		Provider       string  `json:"provider"`
		MismatchReason string  `json:"reason"`
		Note           string  `json:"note"`
		ApproverUserID *string `json:"approverUserId"`
		ApproverTeamID *string `json:"approverTeamId"`
	}
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.Request(r.Context(), caller(w, r), principal(r), application.NewSession{DeviceID: b.DeviceID, TicketID: b.TicketID,
		Provider: b.Provider, MismatchReason: b.MismatchReason, Note: b.Note, Approver: application.Approver{UserID: b.ApproverUserID, TeamID: b.ApproverTeamID}})
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toSession(out))
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	limit, err := httpx.ParseLimit(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "remoteaccess.invalid_limit", "The limit must be a positive integer.")
		return
	}
	q := r.URL.Query()
	res, err := h.svc.ListSessions(r.Context(), principal(r), application.Filter{Status: q.Get("status"), DeviceID: q.Get("deviceId"),
		TicketID: q.Get("ticketId"), InitiatedBy: q.Get("initiatedBy"), Page: application.Page{Limit: limit, Cursor: q.Get("cursor")}})
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	items := make([]sessionDTO, 0, len(res.Items))
	for _, s := range res.Items {
		items = append(items, toSession(s))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "nextCursor": res.NextCursor})
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	d, err := h.svc.GetSession(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	tr := make([]transitionDTO, 0, len(d.Transitions))
	for _, t := range d.Transitions {
		tr = append(tr, transitionDTO{ID: t.ID, FromStatus: t.FromStatus, ToStatus: t.ToStatus, Operation: t.Operation, Reason: t.Reason,
			ActorUserID: t.ActorUserID, ActorSystem: t.ActorSystem, CreatedAt: ts(t.CreatedAt)})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"session": toSession(d.Session), "transitions": tr})
}

// versionBody carries the optimistic-concurrency version and a reason code or decision.
type versionBody struct {
	ExpectedVersion *int   `json:"expectedVersion"`
	Reason          string `json:"reason"`
	Decision        string `json:"decision"`
}

func (h *handler) withReason(fn func(ctx context.Context, c application.Caller, p application.Principal, id string, v *int, reason string) (application.Session, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b versionBody
		if !decode(w, r, &b) {
			return
		}
		out, err := fn(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.Reason)
		if err != nil {
			h.writeErr(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, toSession(out))
	}
}

func (h *handler) consent(w http.ResponseWriter, r *http.Request) {
	var b versionBody
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.RecordConsent(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.Decision)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toSession(out))
}

// launch returns the one-time handle token. It is shown once; only its hash is stored.
func (h *handler) launch(w http.ResponseWriter, r *http.Request) {
	var b versionBody
	if !decode(w, r, &b) {
		return
	}
	hd, err := h.svc.Launch(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"token": hd.Token, "expiresAt": ts(hd.ExpiresAt)})
}

// exchange resolves the token into the provider launch link, in the body only, once.
func (h *handler) exchange(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.Exchange(r.Context(), caller(w, r), principal(r), b.Token)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"sessionId": out.SessionID, "reference": out.Reference, "provider": out.Provider,
		"launchUri": out.URI.Reveal(), "session": toSession(out.Session)})
}
