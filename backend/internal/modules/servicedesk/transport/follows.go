package transport

import (
	"net/http"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

func (h *handler) followState(w http.ResponseWriter, r *http.Request) {
	following, err := h.svc.Following(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]bool{"following": following})
}

func (h *handler) follow(w http.ResponseWriter, r *http.Request)   { h.setFollow(w, r, true) }
func (h *handler) unfollow(w http.ResponseWriter, r *http.Request) { h.setFollow(w, r, false) }

func (h *handler) setFollow(w http.ResponseWriter, r *http.Request, on bool) {
	following, err := h.svc.SetFollowing(r.Context(), caller(w, r), principal(r), r.PathValue("id"), on)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]bool{"following": following})
}
