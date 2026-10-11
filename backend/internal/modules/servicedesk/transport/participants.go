package transport

import (
	"net/http"
	"time"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/servicedesk/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

// Ticket participants (F14 slice A-G): who a Ticket is shared with among the external accounts. Work access in the
// Ticket's Queue shares and unshares; view access lists. An external account itself reaches only
// GET /tickets/{id} and POST /tickets/{id}/comments (the module catalog's allow-list).

type participantDTO struct {
	UserID      string  `json:"userId"`
	DisplayName string  `json:"displayName"`
	Level       string  `json:"level"`
	AddedBy     string  `json:"addedBy"`
	AddedAt     string  `json:"addedAt"`
	ExpiresAt   *string `json:"expiresAt"`
}

func toParticipant(p application.Participant) participantDTO {
	return participantDTO{UserID: p.UserID, DisplayName: p.DisplayName, Level: p.Level, AddedBy: p.AddedBy, AddedAt: ts(p.AddedAt), ExpiresAt: tsPtr(p.ExpiresAt)}
}

func (h *handler) participants(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.Participants(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]participantDTO, 0, len(list))
	for _, p := range list {
		out = append(out, toParticipant(p))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *handler) addParticipant(w http.ResponseWriter, r *http.Request) {
	var b struct {
		UserID    string  `json:"userId"`
		Level     string  `json:"level"`
		ExpiresAt *string `json:"expiresAt"`
	}
	if !decode(w, r, &b) {
		return
	}
	var expires *time.Time
	if b.ExpiresAt != nil {
		at, err := time.Parse(time.RFC3339, *b.ExpiresAt)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "tickets.invalid_request", "expiresAt must be an RFC 3339 timestamp.")
			return
		}
		expires = &at
	}
	p, err := h.svc.AddParticipant(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.UserID, b.Level, expires)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toParticipant(p))
}

func (h *handler) removeParticipant(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.RemoveParticipant(r.Context(), caller(w, r), principal(r), r.PathValue("id"), r.PathValue("userId")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) participantCandidates(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.ParticipantCandidates(r.Context(), principal(r), r.PathValue("id"), r.URL.Query().Get("q"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type candidate struct {
		UserID      string `json:"userId"`
		DisplayName string `json:"displayName"`
		PartyName   string `json:"partyName"`
	}
	out := make([]candidate, 0, len(list))
	for _, a := range list {
		out = append(out, candidate{UserID: a.UserID, DisplayName: a.DisplayName, PartyName: a.PartyName})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}
