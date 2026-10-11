package transport

import (
	"log/slog"
	"net/http"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/organization/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

type partyHandler struct {
	parties *application.ExternalParties
	logger  *slog.Logger
}

// RegisterExternalParties mounts the External Party routes (F14 slice A-G). Every route needs
// organization.external_parties.manage; the application layer checks it again from the caller.
func RegisterExternalParties(mux *http.ServeMux, parties *application.ExternalParties, auth authorization.Authenticator, logger *slog.Logger) {
	h := &partyHandler{parties: parties, logger: logger}
	manage := authorization.Require(auth, permExternalParties)
	route := func(pattern string, fn http.HandlerFunc) {
		mux.Handle(pattern, httpx.NoStore(manage(fn)))
	}
	route("GET /api/v1/external-parties", h.list)
	route("POST /api/v1/external-parties", h.create)
	route("GET /api/v1/external-parties/{id}", h.get)
	route("PATCH /api/v1/external-parties/{id}", h.update)
}

func (h *partyHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	failPeople(h.logger, w, r, err)
}

type partyDTO struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	SponsorUserID string `json:"sponsorUserId"`
	SponsorName   string `json:"sponsorName"`
	Active        bool   `json:"active"`
	Notes         string `json:"notes"`
	Version       int    `json:"version"`
	AccountCount  int    `json:"accountCount"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt"`
}

type partyAccountDTO struct {
	UserID          string `json:"userId"`
	DisplayName     string `json:"displayName"`
	Status          string `json:"status"`
	AccessExpiresAt string `json:"accessExpiresAt"`
}

func toParty(p application.ExternalParty) partyDTO {
	return partyDTO{ID: p.ID, Kind: p.Kind, Name: p.Name, SponsorUserID: p.SponsorUserID, SponsorName: p.SponsorName, Active: p.Active,
		Notes: p.Notes, Version: p.Version, AccountCount: p.AccountCount, CreatedAt: ts(p.CreatedAt), UpdatedAt: ts(p.UpdatedAt)}
}

func (h *partyHandler) list(w http.ResponseWriter, r *http.Request) {
	items, err := h.parties.List(r.Context(), caller(w, r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]partyDTO, 0, len(items))
	for _, p := range items {
		out = append(out, toParty(p))
	}
	ok(w, map[string]any{"items": out, "kinds": application.PartyKinds()})
}

func (h *partyHandler) get(w http.ResponseWriter, r *http.Request) {
	p, accounts, err := h.parties.Get(r.Context(), caller(w, r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	list := make([]partyAccountDTO, 0, len(accounts))
	for _, a := range accounts {
		list = append(list, partyAccountDTO{UserID: a.UserID, DisplayName: a.DisplayName, Status: a.Status, AccessExpiresAt: ts(a.AccessExpiresAt)})
	}
	ok(w, struct {
		partyDTO
		Accounts []partyAccountDTO `json:"accounts"`
	}{toParty(p), list})
}

func (h *partyHandler) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind          string `json:"kind"`
		Name          string `json:"name"`
		SponsorUserID string `json:"sponsorUserId"`
		Notes         string `json:"notes"`
	}
	if !peopleDecode(w, r, &body) {
		return
	}
	p, err := h.parties.Create(r.Context(), caller(w, r), application.NewExternalPartyInput{Kind: body.Kind, Name: body.Name,
		SponsorUserID: body.SponsorUserID, Notes: body.Notes})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toParty(p))
}

func (h *partyHandler) update(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ExpectedVersion int     `json:"expectedVersion"`
		Kind            *string `json:"kind"`
		Name            *string `json:"name"`
		SponsorUserID   *string `json:"sponsorUserId"`
		Notes           *string `json:"notes"`
		Active          *bool   `json:"active"`
	}
	if !peopleDecode(w, r, &body) {
		return
	}
	p, err := h.parties.Update(r.Context(), caller(w, r), r.PathValue("id"), application.ExternalPartyChange{ExpectedVersion: body.ExpectedVersion,
		Kind: body.Kind, Name: body.Name, SponsorUserID: body.SponsorUserID, Notes: body.Notes, Active: body.Active})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ok(w, toParty(p))
}
