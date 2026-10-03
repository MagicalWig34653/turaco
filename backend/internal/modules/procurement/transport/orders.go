package transport

import (
	"net/http"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/procurement/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
)

type orderDTO struct {
	ID           string  `json:"id"`
	Reference    string  `json:"reference"`
	SupplierID   string  `json:"supplierId"`
	Status       string  `json:"status"`
	StatusReason *string `json:"statusReason"`
	Currency     string  `json:"currency"`
	Notes        *string `json:"notes"`
	CreatedBy    *string `json:"createdBy"`
	SentAt       *string `json:"sentAt"`
	ClosedAt     *string `json:"closedAt"`
	Version      int     `json:"version"`
	CreatedAt    string  `json:"createdAt"`
	UpdatedAt    string  `json:"updatedAt"`
}

func toOrder(o application.Order) orderDTO {
	return orderDTO{ID: o.ID, Reference: o.Reference, SupplierID: o.SupplierID, Status: o.Status, StatusReason: o.StatusReason, Currency: o.Currency,
		Notes: o.Notes, CreatedBy: o.CreatedBy, SentAt: tsPtr(o.SentAt), ClosedAt: tsPtr(o.ClosedAt), Version: o.Version,
		CreatedAt: ts(o.CreatedAt), UpdatedAt: ts(o.UpdatedAt)}
}

type lineDTO struct {
	ID               string  `json:"id"`
	LineNo           int     `json:"lineNo"`
	ProductID        string  `json:"productId"`
	Quantity         int     `json:"quantity"`
	UnitPriceCents   int64   `json:"unitPriceCents"`
	ReceivedQuantity int     `json:"receivedQuantity"`
	RequestID        *string `json:"requestId"`
}

func toLine(l application.Line) lineDTO {
	return lineDTO{ID: l.ID, LineNo: l.LineNo, ProductID: l.ProductID, Quantity: l.Quantity, UnitPriceCents: l.UnitPriceCents,
		ReceivedQuantity: l.ReceivedQuantity, RequestID: l.RequestID}
}

type orderDetailDTO struct {
	orderDTO
	Lines             []lineDTO         `json:"lines"`
	TotalCents        int64             `json:"totalCents"`
	SupplierName      string            `json:"supplierName"`
	ProductNames      map[string]string `json:"productNames"`
	AllowedOperations []string          `json:"allowedOperations"`
	LinesEditable     bool              `json:"linesEditable"`
}

func (h *handler) listOrders(w http.ResponseWriter, r *http.Request) {
	page, ok := parsePage(w, r)
	if !ok {
		return
	}
	v := r.URL.Query()
	res, err := h.svc.ListOrders(r.Context(), principal(r), application.OrderFilter{Status: v.Get("status"), SupplierID: v.Get("supplierId"), Page: page})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toList(res, toOrder))
}

func (h *handler) getOrder(w http.ResponseWriter, r *http.Request) {
	d, err := h.svc.GetOrder(r.Context(), principal(r), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := orderDetailDTO{orderDTO: toOrder(d.Order), Lines: make([]lineDTO, 0, len(d.Lines)), TotalCents: d.TotalCents,
		SupplierName: d.SupplierName, ProductNames: d.ProductNames, AllowedOperations: d.AllowedOps, LinesEditable: d.LinesEditable && principal(r).Manage}
	for _, l := range d.Lines {
		out.Lines = append(out.Lines, toLine(l))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *handler) createOrder(w http.ResponseWriter, r *http.Request) {
	var b struct {
		SupplierID string `json:"supplierId"`
		Currency   string `json:"currency"`
		Notes      string `json:"notes"`
	}
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.CreateOrder(r.Context(), caller(w, r), principal(r), application.NewOrder{SupplierID: b.SupplierID, Currency: b.Currency, Notes: b.Notes})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toOrder(out))
}

func (h *handler) updateOrder(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int    `json:"expectedVersion"`
		SupplierID      *string `json:"supplierId"`
		Currency        *string `json:"currency"`
		Notes           *string `json:"notes"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.ExpectedVersion == nil {
		httpx.WriteError(w, http.StatusBadRequest, "procurement.invalid_request", "expectedVersion is required.")
		return
	}
	out, err := h.svc.UpdateOrder(r.Context(), caller(w, r), principal(r), r.PathValue("id"), *b.ExpectedVersion,
		application.OrderUpdate{SupplierID: b.SupplierID, Currency: b.Currency, Notes: b.Notes})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toOrder(out))
}

func (h *handler) addLine(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int   `json:"expectedVersion"`
		ProductID       string `json:"productId"`
		Quantity        int    `json:"quantity"`
		UnitPriceCents  int64  `json:"unitPriceCents"`
		RequestID       string `json:"requestId"`
	}
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.AddLine(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, application.NewLine{
		ProductID: b.ProductID, Quantity: b.Quantity, UnitPriceCents: b.UnitPriceCents, RequestID: b.RequestID,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toLine(out))
}

func (h *handler) updateLine(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int  `json:"expectedVersion"`
		Quantity        int   `json:"quantity"`
		UnitPriceCents  int64 `json:"unitPriceCents"`
	}
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.UpdateLine(r.Context(), caller(w, r), principal(r), r.PathValue("id"), r.PathValue("lineId"), b.ExpectedVersion, b.Quantity, b.UnitPriceCents)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toLine(out))
}

func (h *handler) removeLine(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.RemoveLine(r.Context(), caller(w, r), principal(r), r.PathValue("id"), r.PathValue("lineId"), nil); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) submit(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ExpectedVersion *int    `json:"expectedVersion"`
		ApproverUserID  *string `json:"approverUserId"`
		ApproverTeamID  *string `json:"approverTeamId"`
	}
	if !decode(w, r, &b) {
		return
	}
	out, err := h.svc.Submit(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, application.Approver{UserID: b.ApproverUserID, TeamID: b.ApproverTeamID})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toOrder(out))
}

func (h *handler) orderOp(op func(h *handler, w http.ResponseWriter, r *http.Request, b versionBody) (application.Order, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b versionBody
		if !decode(w, r, &b) {
			return
		}
		out, err := op(h, w, r, b)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, toOrder(out))
	}
}

func (h *handler) send(w http.ResponseWriter, r *http.Request) {
	h.orderOp(func(h *handler, w http.ResponseWriter, r *http.Request, b versionBody) (application.Order, error) {
		return h.svc.Send(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion)
	})(w, r)
}

func (h *handler) acknowledge(w http.ResponseWriter, r *http.Request) {
	h.orderOp(func(h *handler, w http.ResponseWriter, r *http.Request, b versionBody) (application.Order, error) {
		return h.svc.Acknowledge(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion)
	})(w, r)
}

func (h *handler) cancel(w http.ResponseWriter, r *http.Request) {
	h.orderOp(func(h *handler, w http.ResponseWriter, r *http.Request, b versionBody) (application.Order, error) {
		return h.svc.Cancel(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.Reason)
	})(w, r)
}

func (h *handler) closeOrder(w http.ResponseWriter, r *http.Request) {
	h.orderOp(func(h *handler, w http.ResponseWriter, r *http.Request, b versionBody) (application.Order, error) {
		return h.svc.Close(r.Context(), caller(w, r), principal(r), r.PathValue("id"), b.ExpectedVersion, b.Reason)
	})(w, r)
}
