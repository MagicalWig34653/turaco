package transport

import (
	"net/http"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/remoteaccess/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

type providerDTO struct {
	Provider     string `json:"provider"`
	Capabilities struct {
		Attended        bool `json:"attended"`
		ObserveSessions bool `json:"observeSessions"`
		CloseSessions   bool `json:"closeSessions"`
	} `json:"capabilities"`
	Mapped bool `json:"mapped"`
	// PeerID is masked beyond its last three characters unless the caller holds remote_access.admin.
	PeerID    string   `json:"peerId,omitempty"`
	Available bool     `json:"available"`
	Reasons   []string `json:"reasons"`
}

func (h *handler) capabilities(w http.ResponseWriter, r *http.Request) {
	c, err := h.svc.Capabilities(r.Context(), principal(r), r.URL.Query().Get("deviceId"))
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	provs := make([]providerDTO, 0, len(c.Providers))
	for _, p := range c.Providers {
		d := providerDTO{Provider: p.Provider, Mapped: p.Mapped, PeerID: p.PeerID, Available: p.Available, Reasons: p.Reasons}
		d.Capabilities.Attended, d.Capabilities.ObserveSessions, d.Capabilities.CloseSessions =
			p.Capabilities.Attended, p.Capabilities.ObserveSessions, p.Capabilities.CloseSessions
		provs = append(provs, d)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"deviceId": c.DeviceID, "enabled": c.Enabled, "deviceKnown": c.Known,
		"observedAt": tsPtr(c.ObservedAt), "lastCheckinAt": tsPtr(c.LastCheckinAt), "stale": c.Stale, "providers": provs})
}

type mappingDTO struct {
	ID          string  `json:"id"`
	DeviceID    string  `json:"deviceId"`
	Provider    string  `json:"provider"`
	PeerID      string  `json:"peerId"`
	Source      string  `json:"source"`
	Reason      *string `json:"reason"`
	MappedBy    *string `json:"mappedBy"`
	MappedAt    string  `json:"mappedAt"`
	ClosedAt    *string `json:"closedAt"`
	CloseReason *string `json:"closeReason"`
}

func toMapping(m application.PeerMapping, admin bool) mappingDTO {
	return mappingDTO{ID: m.ID, DeviceID: m.DeviceID, Provider: m.Provider, PeerID: application.MaskPeerID(m.PeerID, admin), Source: m.Source,
		Reason: m.Reason, MappedBy: m.MappedBy, MappedAt: ts(m.MappedAt), ClosedAt: tsPtr(m.ClosedAt), CloseReason: m.CloseReason}
}

func (h *handler) mappings(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.Mappings(r.Context(), principal(r), r.URL.Query().Get("deviceId"), r.URL.Query().Get("includeClosed") == "true")
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	items := make([]mappingDTO, 0, len(list))
	for _, m := range list {
		// The service already masked the peer id for non-administrators.
		items = append(items, toMapping(m, true))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handler) mapPeer(w http.ResponseWriter, r *http.Request) {
	var b struct {
		DeviceID string `json:"deviceId"`
		Provider string `json:"provider"`
		PeerID   string `json:"peerId"`
		Reason   string `json:"reason"`
	}
	if !decode(w, r, &b) {
		return
	}
	m, err := h.svc.MapPeer(r.Context(), caller(w, r), principal(r), b.DeviceID, b.Provider, b.PeerID, b.Reason)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toMapping(m, true))
}

func (h *handler) unmapPeer(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if err := h.svc.UnmapPeer(r.Context(), caller(w, r), principal(r), q.Get("deviceId"), q.Get("provider"), q.Get("reason")); err != nil {
		h.writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
