package transport

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/authorization"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/httpx"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/views"
)

// Task Board routes (F13 slice Q-D). Every route needs a Task permission as the outer gate; the service decides per
// Board (access through the underlying View's shares) and per card (Task visibility and authorization). Boards the
// caller may not know about answer 404.

type boardHandler struct {
	handler
	boards *application.Boards
}

// RegisterBoards mounts the Task Board routes.
func RegisterBoards(mux *http.ServeMux, boards *application.Boards, svc *application.Service, auth authorization.Authenticator, logger *slog.Logger) {
	h := &boardHandler{handler: handler{svc: svc, logger: logger}, boards: boards}
	anyTask := authorization.RequireAny(auth, application.TaskPermissions...)
	route := func(pattern string, fn http.HandlerFunc) { mux.Handle(pattern, httpx.NoStore(anyTask(fn))) }
	route("GET /api/v1/tasks/boards", h.list)
	route("POST /api/v1/tasks/boards", h.create)
	route("GET /api/v1/tasks/boards/{id}", h.get)
	route("PATCH /api/v1/tasks/boards/{id}", h.update)
	route("POST /api/v1/tasks/boards/{id}/columns", h.columns)
	route("GET /api/v1/tasks/boards/{id}/cards", h.cards)
	route("POST /api/v1/tasks/boards/{id}/moves", h.move)
	route("PUT /api/v1/tasks/boards/{id}/ranks", h.ranks)
	route("POST /api/v1/tasks/boards/{id}/archive", h.archive(true))
	route("POST /api/v1/tasks/boards/{id}/restore", h.archive(false))
}

func (h *boardHandler) failBoard(w http.ResponseWriter, r *http.Request, err error) {
	var vinv *views.InvalidError
	var run *views.RunError
	switch {
	case errors.As(err, &vinv):
		httpx.WriteError(w, http.StatusBadRequest, "tasks.invalid_request", vinv.Message)
	case errors.As(err, &run):
		httpx.WriteError(w, run.Status, run.Code, run.Message)
	case errors.Is(err, application.ErrBoardNotFound), errors.Is(err, views.ErrNotFound), errors.Is(err, views.ErrModuleDisabled):
		httpx.WriteError(w, http.StatusNotFound, "tasks.board_not_found", "The board was not found.")
	case errors.Is(err, application.ErrBoardConflict), errors.Is(err, views.ErrConflict):
		httpx.WriteError(w, http.StatusConflict, "tasks.board_conflict", "The board was changed by someone else; reload and try again.")
	case errors.Is(err, application.ErrBoardArchived), errors.Is(err, views.ErrArchived):
		httpx.WriteError(w, http.StatusConflict, "tasks.board_archived", "The board is archived.")
	case errors.Is(err, application.ErrBoardLimit), errors.Is(err, views.ErrLimitReached):
		httpx.WriteError(w, http.StatusConflict, "tasks.board_limit_reached", "A board limit was reached.")
	case errors.Is(err, application.ErrAnchorInvalid):
		httpx.WriteError(w, http.StatusConflict, "tasks.board_anchor_invalid", "The card to place after is not in the column; reload the board.")
	case errors.Is(err, views.ErrNameTaken):
		httpx.WriteError(w, http.StatusConflict, "views.name_taken", "You already have a view or board with this name.")
	case errors.Is(err, views.ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, "platform.forbidden", "You do not have permission to perform this action.")
	case errors.Is(err, views.ErrSubjectNotFound):
		httpx.WriteError(w, http.StatusNotFound, "views.subject_not_found", "A user, team or role was not found.")
	default:
		h.fail(w, r, err)
	}
}

func viewCaller(w http.ResponseWriter, r *http.Request) views.Caller {
	p, _ := authorization.PrincipalFrom(r.Context())
	return views.Caller{UserID: p.UserID, Permissions: p.Permissions, CorrelationID: httpx.RequestID(w), Header: r.Header}
}

// ---- DTOs ----

type columnDTO struct {
	ID        string `json:"id"`
	Position  int    `json:"position"`
	Title     string `json:"title"`
	MapsTo    string `json:"mapsTo"`
	Collapsed bool   `json:"collapsed"`
	WIPLimit  *int   `json:"wipLimit"`
}

type boardDTO struct {
	ID            string        `json:"id"`
	ViewID        string        `json:"viewId"`
	ViewVersion   int           `json:"viewVersion"`
	Name          string        `json:"name"`
	Description   string        `json:"description"`
	OwnerID       string        `json:"ownerId"`
	OwnerName     string        `json:"ownerName,omitempty"`
	OwnerTeamID   *string       `json:"ownerTeamId"`
	OwnerTeamName *string       `json:"ownerTeamName"`
	Swimlane      string        `json:"swimlane"`
	Version       int           `json:"version"`
	Access        string        `json:"access"`
	CanEdit       bool          `json:"canEdit"`
	Archived      bool          `json:"archived"`
	Filter        *query.Filter `json:"filter"`
	Columns       []columnDTO   `json:"columns"`
	CreatedAt     string        `json:"createdAt"`
	UpdatedAt     string        `json:"updatedAt"`
}

func toColumn(c application.Column) columnDTO {
	return columnDTO{ID: c.ID, Position: c.Position, Title: c.Title, MapsTo: c.MapsTo, Collapsed: c.Collapsed, WIPLimit: c.WIPLimit}
}

func toBoard(b application.BoardView) boardDTO {
	out := boardDTO{ID: b.ID, ViewID: b.ViewID, ViewVersion: b.ViewVersion, Name: b.Name, Description: b.Description, OwnerID: b.OwnerID,
		OwnerName: b.OwnerName, OwnerTeamID: b.OwnerTeamID, OwnerTeamName: b.OwnerTeamName, Swimlane: b.Swimlane, Version: b.Version,
		Access: b.Access, CanEdit: b.CanEdit, Archived: b.ArchivedAt != nil, Filter: b.Filter, Columns: make([]columnDTO, 0, len(b.Columns)),
		CreatedAt: ts(b.CreatedAt), UpdatedAt: ts(b.UpdatedAt)}
	for _, c := range b.Columns {
		out.Columns = append(out.Columns, toColumn(c))
	}
	return out
}

type cardDTO struct {
	taskDTO
	Rank *string `json:"rank"`
}

type columnCardsDTO struct {
	ColumnID    string          `json:"columnId"`
	Items       []cardDTO       `json:"items"`
	NextCursor  string          `json:"nextCursor,omitempty"`
	Count       int             `json:"count"`
	CountCapped bool            `json:"countCapped"`
	WIPLimit    *int            `json:"wipLimit"`
	OverWIP     bool            `json:"overWipLimit"`
	Warnings    []query.Warning `json:"warnings,omitempty"`
}

func toColumnCards(c application.ColumnCards) columnCardsDTO {
	out := columnCardsDTO{ColumnID: c.Column.ID, Items: make([]cardDTO, 0, len(c.Items)), NextCursor: c.NextCursor, Count: c.Count,
		CountCapped: c.CountCapped, WIPLimit: c.Column.WIPLimit, OverWIP: c.OverWIP, Warnings: c.Warnings}
	for _, card := range c.Items {
		out.Items = append(out.Items, cardDTO{taskDTO: toTask(card.TaskView), Rank: card.Rank})
	}
	return out
}

// ---- handlers ----

func (h *boardHandler) list(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			httpx.WriteError(w, http.StatusBadRequest, "tasks.invalid_limit", "The limit must be a positive integer.")
			return
		}
		limit = n
	}
	page, err := h.boards.List(r.Context(), viewCaller(w, r), principal(r), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		h.failBoard(w, r, err)
		return
	}
	out := struct {
		Items      []boardDTO `json:"items"`
		NextCursor string     `json:"nextCursor,omitempty"`
	}{Items: make([]boardDTO, 0, len(page.Items)), NextCursor: page.NextCursor}
	for _, b := range page.Items {
		out.Items = append(out.Items, toBoard(b))
	}
	httpx.JSON(w, http.StatusOK, out)
}

type columnInputBody struct {
	Title     string `json:"title"`
	MapsTo    string `json:"mapsTo"`
	Collapsed bool   `json:"collapsed"`
	WIPLimit  *int   `json:"wipLimit"`
}

type createBoardBody struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Filter      *query.Filter     `json:"filter"`
	TeamID      *string           `json:"teamId"`
	Swimlane    string            `json:"swimlane"`
	Columns     []columnInputBody `json:"columns"`
}

func (h *boardHandler) create(w http.ResponseWriter, r *http.Request) {
	var b createBoardBody
	if !decode(w, r, &b) {
		return
	}
	in := application.CreateBoardInput{Name: b.Name, Description: b.Description, Filter: b.Filter, TeamID: b.TeamID, Swimlane: b.Swimlane}
	for _, c := range b.Columns {
		in.Columns = append(in.Columns, application.ColumnInput{Title: c.Title, MapsTo: c.MapsTo, Collapsed: c.Collapsed, WIPLimit: c.WIPLimit})
	}
	v, err := h.boards.Create(r.Context(), caller(w, r), viewCaller(w, r), principal(r), in)
	if err != nil {
		h.failBoard(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toBoard(v))
}

func (h *boardHandler) get(w http.ResponseWriter, r *http.Request) {
	v, err := h.boards.Get(r.Context(), viewCaller(w, r), principal(r), r.PathValue("id"))
	if err != nil {
		h.failBoard(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toBoard(v))
}

// optionalFilter distinguishes an absent filter (unchanged) from null (cleared).
type optionalFilter struct {
	Set   bool
	Value *query.Filter
}

func (o *optionalFilter) UnmarshalJSON(raw []byte) error {
	o.Set = true
	if string(raw) == "null" {
		return nil
	}
	var f query.Filter
	if err := json.Unmarshal(raw, &f); err != nil {
		return err
	}
	o.Value = &f
	return nil
}

type updateBoardBody struct {
	ExpectedVersion int            `json:"expectedVersion"`
	Name            *string        `json:"name"`
	Description     *string        `json:"description"`
	Swimlane        *string        `json:"swimlane"`
	Filter          optionalFilter `json:"filter"`
}

func (h *boardHandler) update(w http.ResponseWriter, r *http.Request) {
	var b updateBoardBody
	if !decode(w, r, &b) {
		return
	}
	v, err := h.boards.Update(r.Context(), caller(w, r), viewCaller(w, r), principal(r), r.PathValue("id"), application.UpdateBoardInput{
		ExpectedVersion: b.ExpectedVersion, Name: b.Name, Description: b.Description, Swimlane: b.Swimlane, SetFilter: b.Filter.Set, Filter: b.Filter.Value})
	if err != nil {
		h.failBoard(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toBoard(v))
}

type columnOpBody struct {
	ExpectedVersion int     `json:"expectedVersion"`
	Operation       string  `json:"operation"`
	ColumnID        string  `json:"columnId"`
	Title           *string `json:"title"`
	MapsTo          *string `json:"mapsTo"`
	Position        *int    `json:"position"`
	WIPLimit        *int    `json:"wipLimit"`
	Collapsed       *bool   `json:"collapsed"`
}

func (h *boardHandler) columns(w http.ResponseWriter, r *http.Request) {
	var b columnOpBody
	if !decode(w, r, &b) {
		return
	}
	v, err := h.boards.ChangeColumns(r.Context(), caller(w, r), viewCaller(w, r), principal(r), r.PathValue("id"), application.ColumnOp{
		ExpectedVersion: b.ExpectedVersion, Operation: b.Operation, ColumnID: b.ColumnID, Title: b.Title, MapsTo: b.MapsTo,
		Position: b.Position, WIPLimit: b.WIPLimit, Collapsed: b.Collapsed})
	if err != nil {
		h.failBoard(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toBoard(v))
}

func (h *boardHandler) cards(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			httpx.WriteError(w, http.StatusBadRequest, "tasks.invalid_limit", "The limit must be a positive integer.")
			return
		}
		limit = n
	}
	q := r.URL.Query()
	cols, err := h.boards.Cards(r.Context(), viewCaller(w, r), principal(r), r.PathValue("id"), q.Get("column"), q.Get("cursor"), limit)
	if err != nil {
		h.failBoard(w, r, err)
		return
	}
	out := struct {
		Columns []columnCardsDTO `json:"columns"`
	}{Columns: make([]columnCardsDTO, 0, len(cols))}
	for _, c := range cols {
		out.Columns = append(out.Columns, toColumnCards(c))
	}
	httpx.JSON(w, http.StatusOK, out)
}

type moveBody struct {
	TaskID          string `json:"taskId"`
	ExpectedVersion *int   `json:"expectedVersion"`
	ColumnID        string `json:"columnId"`
	Reason          string `json:"reason"`
	AfterTaskID     string `json:"afterTaskId"`
	Top             bool   `json:"top"`
}

func (h *boardHandler) move(w http.ResponseWriter, r *http.Request) {
	var b moveBody
	if !decode(w, r, &b) {
		return
	}
	res, err := h.boards.Move(r.Context(), caller(w, r), viewCaller(w, r), principal(r), r.PathValue("id"), application.MoveInput{
		TaskID: b.TaskID, ExpectedVersion: b.ExpectedVersion, ColumnID: b.ColumnID, Reason: b.Reason, AfterTaskID: b.AfterTaskID, AtTop: b.Top})
	if err != nil {
		h.failBoard(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Task      taskDTO `json:"task"`
		Operation string  `json:"operation,omitempty"`
		Placed    bool    `json:"placed"`
		Rank      *string `json:"rank"`
	}{Task: toTask(res.Task), Operation: res.Operation, Placed: res.Placed, Rank: res.Rank})
}

type ranksBody struct {
	TaskID      string `json:"taskId"`
	ColumnID    string `json:"columnId"`
	AfterTaskID string `json:"afterTaskId"`
	Top         bool   `json:"top"`
}

func (h *boardHandler) ranks(w http.ResponseWriter, r *http.Request) {
	var b ranksBody
	if !decode(w, r, &b) {
		return
	}
	rank, err := h.boards.Place(r.Context(), viewCaller(w, r), principal(r), r.PathValue("id"), application.PlaceInput{
		TaskID: b.TaskID, ColumnID: b.ColumnID, AfterTaskID: b.AfterTaskID, AtTop: b.Top})
	if err != nil {
		h.failBoard(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		TaskID   string `json:"taskId"`
		ColumnID string `json:"columnId"`
		Rank     string `json:"rank"`
	}{TaskID: b.TaskID, ColumnID: b.ColumnID, Rank: rank})
}

func (h *boardHandler) archive(archive bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var b versionBody
		if !decode(w, r, &b) {
			return
		}
		if b.ExpectedVersion == nil {
			httpx.WriteError(w, http.StatusBadRequest, "tasks.invalid_request", "expectedVersion is required.")
			return
		}
		fn := h.boards.Restore
		if archive {
			fn = h.boards.Archive
		}
		v, err := fn(r.Context(), caller(w, r), viewCaller(w, r), principal(r), r.PathValue("id"), *b.ExpectedVersion)
		if err != nil {
			h.failBoard(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, toBoard(v))
	}
}
