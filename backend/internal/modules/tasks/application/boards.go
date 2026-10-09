package application

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/safetext"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/views"
)

// Task Boards (ADR-0033, F13 slice Q-D, docs/product/f13-workbench-views-design.md).
//
// A Board is a Saved View over tasks (name, filter and sharing live in the View: one sharing mechanism) plus ordered
// Columns that map to Task statuses and per-board card ranks. A Board never stores or sets a Task status: a card
// move is the existing Task lifecycle operation (start, block, unblock, complete, cancel, reopen), chosen from the
// card's status and the target column's mapping, with a mandatory expected version. Ranks and column placement are
// presentation data of the Board.
//
// Audit actions: tasks.board.created, .changed, .column_changed, .archived, .restored. Audit metadata carries ids,
// versions and counts, never names, titles or filter values.

// Board limits.
const (
	MinBoardColumns   = 2
	MaxBoardColumns   = 8
	MaxBoardsPerOwner = 20
	MaxColumnTitle    = 40
	MaxColumnWIP      = 999
	// MaxCardsPerColumn is how many cards of a column are loaded for a placement (two query pages); a card below
	// that position is reached by loading more cards first.
	MaxCardsPerColumn = 200
	// MaxRanksPerBoard bounds the number of ranked cards of a Board; ranks of finished tasks are pruned first.
	MaxRanksPerBoard = 5000
	maxBoardIDsBatch = 100
	maxListScans     = 20
)

// Swimlane modes of a Board.
const (
	SwimlaneNone     = "none"
	SwimlaneAssignee = "assignee"
)

// Board column operations.
const (
	ColumnAdd      = "add"
	ColumnRename   = "rename"
	ColumnRemap    = "remap"
	ColumnReorder  = "reorder"
	ColumnRemove   = "remove"
	ColumnSetWIP   = "set_wip"
	ColumnCollapse = "collapse"
)

// Board errors. The transport maps them to stable API codes.
var (
	// ErrBoardNotFound answers every request for a Board the caller may not know about.
	ErrBoardNotFound = errors.New("tasks: board not found")
	// ErrBoardConflict means the Board (or its View) changed since the caller read it.
	ErrBoardConflict = errors.New("tasks: board version conflict")
	// ErrBoardArchived means the Board is archived.
	ErrBoardArchived = errors.New("tasks: board archived")
	// ErrBoardLimit means a Board, column or rank limit was reached.
	ErrBoardLimit = errors.New("tasks: board limit reached")
	// ErrAnchorInvalid means a placement anchor is not a card of the column.
	ErrAnchorInvalid = errors.New("tasks: placement anchor is not a card of the column")
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func isUUID(s string) bool { return uuidPattern.MatchString(s) }

// Column is one Board column.
type Column struct {
	ID       string
	Position int
	Title    string
	// MapsTo is the Task status the column stands for.
	MapsTo    string
	Collapsed bool
	// WIPLimit is a soft limit: exceeding it is reported, never enforced.
	WIPLimit *int
}

// Board is the persisted state of a Board (without the View's name, filter and sharing).
type Board struct {
	ID          string
	ViewID      string
	CreatedBy   string
	OwnerTeamID *string
	Swimlane    string
	Version     int
	ArchivedAt  *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Columns     []Column
}

// firstColumnOf returns the first column (by position) that maps to the status.
func (b Board) firstColumnOf(status string) (Column, bool) {
	for _, c := range b.Columns {
		if c.MapsTo == status {
			return c, true
		}
	}
	return Column{}, false
}

func (b Board) column(id string) (Column, bool) {
	for _, c := range b.Columns {
		if strings.EqualFold(c.ID, id) {
			return c, true
		}
	}
	return Column{}, false
}

// BoardView is a Board as one caller sees it.
type BoardView struct {
	Board
	Name        string
	Description string
	OwnerID     string
	OwnerName   string
	// OwnerTeamName is set for Team-owned Boards.
	OwnerTeamName *string
	// Access is "owner", "edit" or "use".
	Access  string
	CanEdit bool
	// ViewVersion is the version of the underlying View (sharing and filter changes go through the Views API).
	ViewVersion int
	Filter      *query.Filter
}

// Card is a task on a Board.
type Card struct {
	TaskView
	// Rank orders the cards of a column; nil for cards that were never ranked.
	Rank *string
}

// ColumnInput describes a column on creation.
type ColumnInput struct {
	Title     string
	MapsTo    string
	Collapsed bool
	WIPLimit  *int
}

// NewBoard is the input of BoardStore.CreateBoard.
type NewBoard struct {
	ViewID      string
	CreatedBy   string
	OwnerTeamID *string
	Swimlane    string
	Columns     []Column
}

// BoardChange is the outcome of a decision made inside the store's transaction: the desired state (swimlane and the
// full ordered column list; columns without an ID are new) plus audit action and events.
type BoardChange struct {
	Next     Board
	NoChange bool
	Action   string
	Metadata map[string]any
	Events   []Event
}

// CardPlacement tells the card query which column of which Board it lists.
type CardPlacement struct {
	BoardID  string
	ColumnID string
	// First is true when the column is the first (by position) that maps to its status: cards without a placement
	// of their own belong to it.
	First bool
}

// RankTx is the set of rank operations inside one locked Board rank transaction.
type RankTx interface {
	// Max and Min return the highest and lowest rank of the Board.
	Max(ctx context.Context) (string, bool, error)
	Min(ctx context.Context) (string, bool, error)
	// Successor and Predecessor return the next higher or lower rank than rank.
	Successor(ctx context.Context, rank string) (string, bool, error)
	Predecessor(ctx context.Context, rank string) (string, bool, error)
	// Put stores the rank of a task and, when columnID is not empty, its column placement.
	Put(ctx context.Context, taskID, rank, columnID string) error
	// Count returns the number of ranked cards of the Board.
	Count(ctx context.Context) (int, error)
	// PruneTerminal deletes the ranks of completed and cancelled tasks and returns how many.
	PruneTerminal(ctx context.Context) (int, error)
	// Rebalance reassigns evenly spread ranks in the current order.
	Rebalance(ctx context.Context) error
}

// BoardStore is the persistence port of Boards; the repository implements it.
type BoardStore interface {
	// CreateBoard inserts the Board with its columns, audit and event in one transaction, serialized per creator for
	// the per-owner limit (ErrBoardLimit).
	CreateBoard(ctx context.Context, c Caller, n NewBoard) (Board, error)
	// GetBoard returns a Board with its columns; ErrBoardNotFound for an unknown or malformed id.
	GetBoard(ctx context.Context, id string) (Board, error)
	// ListBoards returns active Boards with id below afterID (newest first), at most limit.
	ListBoards(ctx context.Context, afterID string, limit int) ([]Board, error)
	// ChangeBoard locks the Board row (FOR UPDATE), calls decide with its current state and, unless decide fails or
	// returns NoChange, stores Next with version+1 and records audit and events in the same transaction.
	ChangeBoard(ctx context.Context, c Caller, id string, decide func(cur Board) (BoardChange, error)) (Board, error)
	// SetArchived archives or restores a Board (audited, with event) after checking the expected version.
	SetArchived(ctx context.Context, c Caller, id string, expected int, archived bool) (Board, error)
	// QueryBoardCards runs a compiled card plan. With a placement the cards are joined with their ranks, ordered
	// rank-first and limited to the column; without one the plan lists plain tasks (membership checks).
	QueryBoardCards(ctx context.Context, plan *query.Plan, vis query.Fragment, place *CardPlacement) (query.Page[Card], error)
	// WithRankLock runs fn in a transaction that holds the Board's rank lock, so rank writes are serialized.
	WithRankLock(ctx context.Context, boardID string, fn func(tx RankTx) error) error
	// ClearPlacement removes the column placement (not the rank) of one card.
	ClearPlacement(ctx context.Context, boardID, taskID string) error
	// BoardsOfViews maps View ids to the id of the active Board that uses them.
	BoardsOfViews(ctx context.Context, viewIDs []string) (map[string]string, error)
}

// BoardViews is the part of the Saved Views service Boards build on (implemented by platform/views.Service).
type BoardViews interface {
	Create(ctx context.Context, c views.Caller, in views.CreateInput) (views.ViewInfo, error)
	Update(ctx context.Context, c views.Caller, id string, in views.UpdateInput) (views.ViewInfo, error)
	SetShares(ctx context.Context, c views.Caller, id string, expected int, in []views.ShareInput) (views.ViewInfo, error)
	Archive(ctx context.Context, c views.Caller, id string, expectedVersion int) (views.ViewInfo, error)
	Restore(ctx context.Context, c views.Caller, id string, expectedVersion int) (views.ViewInfo, error)
	Resolve(ctx context.Context, c views.Caller, id string) (views.Resolved, error)
	ResolveMany(ctx context.Context, c views.Caller, ids []string) (map[string]views.Resolved, error)
}

// Boards performs Board operations on top of the Task service and the Saved Views.
type Boards struct {
	svc   *Service
	store BoardStore
	views BoardViews
	locks boardLocks
}

// NewBoards creates the Board service.
func NewBoards(svc *Service, store BoardStore, v BoardViews) *Boards {
	return &Boards{svc: svc, store: store, views: v}
}

// ---- access ----

// loaded is a Board together with the caller's access to it.
type loaded struct {
	BoardView
	res views.Resolved
}

func (b *Boards) canUseTasks(p Principal) error {
	if p.UserID == "" || !(p.ViewAll || p.Manage || p.Work) {
		return ErrForbidden
	}
	return nil
}

// load reads a Board the caller may use. Unknown Boards, Boards of Views the caller has no share for, archived
// Boards of other Users and Boards of an unreadable resource are one answer: ErrBoardNotFound.
func (b *Boards) load(ctx context.Context, vc views.Caller, p Principal, a access, id string) (loaded, error) {
	if err := b.canUseTasks(p); err != nil {
		return loaded{}, err
	}
	board, err := b.store.GetBoard(ctx, id)
	if err != nil {
		return loaded{}, err
	}
	return b.attach(ctx, vc, p, a, board)
}

func (b *Boards) attach(ctx context.Context, vc views.Caller, p Principal, a access, board Board) (loaded, error) {
	res, err := b.views.Resolve(ctx, vc, board.ViewID)
	if err != nil {
		if errors.Is(err, views.ErrNotFound) || errors.Is(err, views.ErrModuleDisabled) {
			return loaded{}, ErrBoardNotFound
		}
		return loaded{}, err
	}
	if !res.CanRun() {
		return loaded{}, ErrBoardNotFound
	}
	if board.ArchivedAt != nil && res.Access != views.AccessOwner {
		return loaded{}, ErrBoardNotFound
	}
	out := loaded{res: res}
	out.Board = board
	out.Name, out.Description = res.Name, res.Description
	out.OwnerID, out.OwnerName = res.OwnerID, res.OwnerName
	out.ViewVersion = res.Version
	out.Filter = res.Definition.Filter
	switch res.Access {
	case views.AccessOwner:
		out.Access, out.CanEdit = "owner", true
	case views.AccessEdit:
		out.Access, out.CanEdit = "edit", true
	default:
		out.Access = "use"
		// Members of the owning Team edit a Team Board with tasks.boards.manage_team or tasks.manage.
		if board.OwnerTeamID != nil && (p.BoardsManageTeam || p.Manage) {
			if _, member := a.teams[*board.OwnerTeamID]; member {
				out.Access, out.CanEdit = "edit", true
			}
		}
	}
	return out, nil
}

// requireEdit loads the Board and requires edit access; a caller who may only use it gets ErrForbidden.
func (b *Boards) requireEdit(ctx context.Context, vc views.Caller, p Principal, a access, id string) (loaded, error) {
	ld, err := b.load(ctx, vc, p, a, id)
	if err != nil {
		return loaded{}, err
	}
	if !ld.CanEdit {
		return loaded{}, ErrForbidden
	}
	if ld.ArchivedAt != nil || ld.res.ArchivedAt != nil {
		return loaded{}, ErrBoardArchived
	}
	return ld, nil
}

func (b *Boards) teamName(ctx context.Context, bv *BoardView) error {
	if bv.OwnerTeamID == nil {
		return nil
	}
	names, err := b.svc.dir.TeamNames(ctx, []string{*bv.OwnerTeamID})
	if err != nil {
		return fmt.Errorf("resolve team name: %w", err)
	}
	if n, ok := names[*bv.OwnerTeamID]; ok {
		bv.OwnerTeamName = &n
	}
	return nil
}

// ---- validation ----

func cleanColumnTitle(s string) (string, error) {
	s = strings.TrimSpace(s)
	if n := utf8.RuneCountInString(s); n < 1 || n > MaxColumnTitle || !utf8.ValidString(s) {
		return "", invalid("a column title must be 1-%d characters", MaxColumnTitle)
	}
	if safetext.ContainsUnsafe(s, false) {
		return "", invalid("a column title must not contain control or invisible formatting characters")
	}
	return s, nil
}

func validMapsTo(s string) error {
	if !contains(statuses, s) {
		return invalid("a column maps to one of open, in_progress, blocked, completed, cancelled")
	}
	return nil
}

func validWIP(w *int) error {
	if w != nil && (*w < 1 || *w > MaxColumnWIP) {
		return invalid("a WIP limit is between 1 and %d", MaxColumnWIP)
	}
	return nil
}

func validSwimlane(s string) error {
	if s != SwimlaneNone && s != SwimlaneAssignee {
		return invalid("swimlane must be none or assignee")
	}
	return nil
}

// DefaultColumns are the columns of a new Board without an explicit list.
func DefaultColumns() []ColumnInput {
	return []ColumnInput{
		{Title: "Open", MapsTo: StatusOpen},
		{Title: "In progress", MapsTo: StatusInProgress},
		{Title: "Blocked", MapsTo: StatusBlocked},
		{Title: "Done", MapsTo: StatusCompleted},
	}
}

func cleanColumns(in []ColumnInput) ([]Column, error) {
	if len(in) == 0 {
		in = DefaultColumns()
	}
	if len(in) < MinBoardColumns || len(in) > MaxBoardColumns {
		return nil, invalid("a board has %d to %d columns", MinBoardColumns, MaxBoardColumns)
	}
	out := make([]Column, 0, len(in))
	for i, c := range in {
		title, err := cleanColumnTitle(c.Title)
		if err != nil {
			return nil, err
		}
		if err := validMapsTo(c.MapsTo); err != nil {
			return nil, err
		}
		if err := validWIP(c.WIPLimit); err != nil {
			return nil, err
		}
		out = append(out, Column{Position: i, Title: title, MapsTo: c.MapsTo, Collapsed: c.Collapsed, WIPLimit: c.WIPLimit})
	}
	return out, nil
}

// ---- create, read ----

// CreateBoardInput is the input of Create.
type CreateBoardInput struct {
	Name        string
	Description string
	// Filter selects the tasks of the Board (any task filter); nil means every task the viewer may see.
	Filter *query.Filter
	// TeamID makes the Board Team-owned: it is shown to the Team's members.
	TeamID   *string
	Swimlane string // empty means none
	Columns  []ColumnInput
}

func withShare(vc views.Caller) views.Caller {
	perms := make(map[string]struct{}, len(vc.Permissions)+1)
	for k := range vc.Permissions {
		perms[k] = struct{}{}
	}
	perms[views.PermShare] = struct{}{}
	vc.Permissions = perms
	return vc
}

// Create creates a private Board owned by the caller (shared with the Team's members when TeamID is set). The
// underlying View is created first, so name, filter and permissions are validated by the Views platform exactly as
// for any other View of tasks.
func (b *Boards) Create(ctx context.Context, c Caller, vc views.Caller, p Principal, in CreateBoardInput) (BoardView, error) {
	if err := c.validate(); err != nil {
		return BoardView{}, err
	}
	if err := b.canUseTasks(p); err != nil {
		return BoardView{}, err
	}
	cols, err := cleanColumns(in.Columns)
	if err != nil {
		return BoardView{}, err
	}
	swimlane := in.Swimlane
	if swimlane == "" {
		swimlane = SwimlaneNone
	}
	if err := validSwimlane(swimlane); err != nil {
		return BoardView{}, err
	}
	if in.TeamID != nil {
		if !(p.BoardsManageTeam || p.Manage) {
			return BoardView{}, ErrForbidden
		}
		if err := checkAssignees(ctx, b.svc.dir, nil, in.TeamID); err != nil {
			return BoardView{}, err
		}
	}
	info, err := b.views.Create(ctx, vc, views.CreateInput{Resource: taskCatalog.Key(), Name: in.Name, Description: in.Description,
		Definition: views.Definition{Filter: in.Filter}})
	if err != nil {
		return BoardView{}, err
	}
	board, err := b.store.CreateBoard(ctx, c, NewBoard{ViewID: info.ID, CreatedBy: c.Actor.UserID, OwnerTeamID: in.TeamID, Swimlane: swimlane, Columns: cols})
	if err != nil {
		// The View is useless without its Board; archive it (best effort, it stays a plain View otherwise).
		_, _ = b.views.Archive(ctx, vc, info.ID, info.Version)
		return BoardView{}, err
	}
	if in.TeamID != nil {
		if _, err := b.views.SetShares(ctx, withShare(vc), info.ID, info.Version, []views.ShareInput{
			{SubjectType: views.SubjectTeam, SubjectID: *in.TeamID, Level: views.LevelUse}}); err != nil {
			return BoardView{}, fmt.Errorf("share team board with its team: %w", err)
		}
	}
	a, err := b.svc.access(ctx, p)
	if err != nil {
		return BoardView{}, err
	}
	ld, err := b.attach(ctx, vc, p, a, board)
	if err != nil {
		return BoardView{}, err
	}
	if err := b.teamName(ctx, &ld.BoardView); err != nil {
		return BoardView{}, err
	}
	return ld.BoardView, nil
}

// Get returns a Board the caller may use.
func (b *Boards) Get(ctx context.Context, vc views.Caller, p Principal, id string) (BoardView, error) {
	a, err := b.svc.access(ctx, p)
	if err != nil {
		return BoardView{}, err
	}
	ld, err := b.load(ctx, vc, p, a, id)
	if err != nil {
		return BoardView{}, err
	}
	if err := b.teamName(ctx, &ld.BoardView); err != nil {
		return BoardView{}, err
	}
	return ld.BoardView, nil
}

// BoardPage is one page of Boards.
type BoardPage struct {
	Items      []BoardView
	NextCursor string
}

// List returns the Boards the caller may use, newest first. The cursor is the id of the last Board returned.
func (b *Boards) List(ctx context.Context, vc views.Caller, p Principal, cursor string, limit int) (BoardPage, error) {
	if err := b.canUseTasks(p); err != nil {
		return BoardPage{}, err
	}
	if cursor != "" && !isUUID(cursor) {
		return BoardPage{}, invalid("the cursor is invalid")
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	a, err := b.svc.access(ctx, p)
	if err != nil {
		return BoardPage{}, err
	}
	out := BoardPage{Items: []BoardView{}}
	after := cursor
	for scans := 0; scans < maxListScans; scans++ {
		batch, err := b.store.ListBoards(ctx, after, maxBoardIDsBatch)
		if err != nil {
			return BoardPage{}, err
		}
		if len(batch) == 0 {
			return out, nil
		}
		ids := make([]string, len(batch))
		for i, bd := range batch {
			ids[i] = bd.ViewID
		}
		resolved, err := b.views.ResolveMany(ctx, vc, ids)
		if err != nil {
			return BoardPage{}, err
		}
		for _, bd := range batch {
			after = bd.ID
			if _, ok := resolved[bd.ViewID]; !ok {
				continue
			}
			ld, err := b.attach(ctx, vc, p, a, bd)
			if errors.Is(err, ErrBoardNotFound) {
				continue
			}
			if err != nil {
				return BoardPage{}, err
			}
			if len(out.Items) == limit {
				out.NextCursor = out.Items[len(out.Items)-1].ID
				return b.withTeamNames(ctx, out)
			}
			out.Items = append(out.Items, ld.BoardView)
		}
		if len(batch) < maxBoardIDsBatch {
			return b.withTeamNames(ctx, out)
		}
	}
	// The scan budget is spent: continue from the last examined Board.
	if len(out.Items) > 0 || after != cursor {
		out.NextCursor = after
	}
	return b.withTeamNames(ctx, out)
}

func (b *Boards) withTeamNames(ctx context.Context, page BoardPage) (BoardPage, error) {
	var ids []string
	for _, bv := range page.Items {
		if bv.OwnerTeamID != nil {
			ids = append(ids, *bv.OwnerTeamID)
		}
	}
	if len(ids) == 0 {
		return page, nil
	}
	names, err := b.svc.dir.TeamNames(ctx, ids)
	if err != nil {
		return BoardPage{}, fmt.Errorf("resolve team names: %w", err)
	}
	for i := range page.Items {
		if t := page.Items[i].OwnerTeamID; t != nil {
			if n, ok := names[*t]; ok {
				page.Items[i].OwnerTeamName = &n
			}
		}
	}
	return page, nil
}

// AnnotatePins marks pinned Views that are Boards (implements views.PinAnnotator).
func (b *Boards) AnnotatePins(ctx context.Context, viewIDs []string) (map[string]views.PinAnnotation, error) {
	byView, err := b.store.BoardsOfViews(ctx, viewIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[string]views.PinAnnotation, len(byView))
	for viewID, boardID := range byView {
		out[viewID] = views.PinAnnotation{Kind: "board", Ref: boardID}
	}
	return out, nil
}

// ---- change ----

// UpdateBoardInput changes a Board; nil fields stay unchanged.
type UpdateBoardInput struct {
	ExpectedVersion int
	Name            *string
	Description     *string
	Swimlane        *string
	// SetFilter replaces the Board's filter with Filter (nil Filter selects every visible task).
	SetFilter bool
	Filter    *query.Filter
}

// Update changes the name, description, filter and swimlane mode of a Board (owner or editor). The expected
// version is the Board's; the underlying View is edited under its own current version.
func (b *Boards) Update(ctx context.Context, c Caller, vc views.Caller, p Principal, id string, in UpdateBoardInput) (BoardView, error) {
	if err := c.validate(); err != nil {
		return BoardView{}, err
	}
	if in.ExpectedVersion < 1 {
		return BoardView{}, invalid("expectedVersion is required")
	}
	if in.Name == nil && in.Description == nil && in.Swimlane == nil && !in.SetFilter {
		return BoardView{}, invalid("at least one field to change is required")
	}
	if in.Swimlane != nil {
		if err := validSwimlane(*in.Swimlane); err != nil {
			return BoardView{}, err
		}
	}
	a, err := b.svc.access(ctx, p)
	if err != nil {
		return BoardView{}, err
	}
	ld, err := b.requireEdit(ctx, vc, p, a, id)
	if err != nil {
		return BoardView{}, err
	}
	if ld.Version != in.ExpectedVersion {
		return BoardView{}, ErrBoardConflict
	}
	var changed []string
	if in.Name != nil || in.Description != nil || in.SetFilter {
		up := views.UpdateInput{ExpectedVersion: ld.res.Version, Name: in.Name, Description: in.Description}
		if in.SetFilter {
			up.Definition = &views.Definition{Filter: in.Filter, Columns: ld.res.Definition.Columns}
		}
		if _, err := b.views.Update(ctx, vc, ld.ViewID, up); err != nil {
			return BoardView{}, err
		}
		if in.Name != nil {
			changed = append(changed, "name")
		}
		if in.Description != nil {
			changed = append(changed, "description")
		}
		if in.SetFilter {
			changed = append(changed, "filter")
		}
	}
	if in.Swimlane != nil && *in.Swimlane != ld.Swimlane {
		changed = append(changed, "swimlane")
	}
	if len(changed) == 0 {
		return ld.BoardView, nil
	}
	if _, err := b.store.ChangeBoard(ctx, c, id, func(cur Board) (BoardChange, error) {
		if cur.ArchivedAt != nil {
			return BoardChange{}, ErrBoardArchived
		}
		if cur.Version != in.ExpectedVersion {
			return BoardChange{}, ErrBoardConflict
		}
		next := cur
		if in.Swimlane != nil {
			next.Swimlane = *in.Swimlane
		}
		return BoardChange{Next: next, Action: "tasks.board.changed", Metadata: map[string]any{"changedFields": changed, "viewId": cur.ViewID}}, nil
	}); err != nil {
		return BoardView{}, err
	}
	return b.Get(ctx, vc, p, id)
}

// ColumnOp is one column operation of ChangeColumns.
type ColumnOp struct {
	ExpectedVersion int
	Operation       string
	ColumnID        string
	Title           *string
	MapsTo          *string
	// Position is the new index for add (default: last) and reorder.
	Position *int
	// WIPLimit is the new soft limit for set_wip; nil clears it.
	WIPLimit  *int
	Collapsed *bool
}

// ChangeColumns applies one explicit column operation (add, rename, remap, reorder, remove, set_wip, collapse) to
// a Board the caller may edit. Cards are never touched: changing a mapping only changes where they are listed.
func (b *Boards) ChangeColumns(ctx context.Context, c Caller, vc views.Caller, p Principal, id string, op ColumnOp) (BoardView, error) {
	if err := c.validate(); err != nil {
		return BoardView{}, err
	}
	if op.ExpectedVersion < 1 {
		return BoardView{}, invalid("expectedVersion is required")
	}
	a, err := b.svc.access(ctx, p)
	if err != nil {
		return BoardView{}, err
	}
	if _, err := b.requireEdit(ctx, vc, p, a, id); err != nil {
		return BoardView{}, err
	}
	var title string
	if op.Title != nil {
		if title, err = cleanColumnTitle(*op.Title); err != nil {
			return BoardView{}, err
		}
	}
	if op.MapsTo != nil {
		if err := validMapsTo(*op.MapsTo); err != nil {
			return BoardView{}, err
		}
	}
	if op.Operation == ColumnSetWIP {
		if err := validWIP(op.WIPLimit); err != nil {
			return BoardView{}, err
		}
	}
	if _, err := b.store.ChangeBoard(ctx, c, id, func(cur Board) (BoardChange, error) {
		if cur.ArchivedAt != nil {
			return BoardChange{}, ErrBoardArchived
		}
		if cur.Version != op.ExpectedVersion {
			return BoardChange{}, ErrBoardConflict
		}
		cols := slices.Clone(cur.Columns)
		idx := slices.IndexFunc(cols, func(col Column) bool { return strings.EqualFold(col.ID, op.ColumnID) })
		needColumn := op.Operation != ColumnAdd
		if needColumn && idx < 0 {
			return BoardChange{}, invalid("unknown column")
		}
		meta := map[string]any{"operation": op.Operation}
		if needColumn {
			meta["columnId"] = cols[idx].ID
		}
		switch op.Operation {
		case ColumnAdd:
			if op.Title == nil || op.MapsTo == nil {
				return BoardChange{}, invalid("title and mapsTo are required to add a column")
			}
			if len(cols) >= MaxBoardColumns {
				return BoardChange{}, ErrBoardLimit
			}
			at := len(cols)
			if op.Position != nil {
				at = max(0, min(*op.Position, len(cols)))
			}
			cols = slices.Insert(cols, at, Column{Title: title, MapsTo: *op.MapsTo, WIPLimit: nil})
			meta["mapsTo"] = *op.MapsTo
		case ColumnRename:
			if op.Title == nil {
				return BoardChange{}, invalid("title is required")
			}
			cols[idx].Title = title
		case ColumnRemap:
			if op.MapsTo == nil {
				return BoardChange{}, invalid("mapsTo is required")
			}
			meta["from"], meta["mapsTo"] = cols[idx].MapsTo, *op.MapsTo
			cols[idx].MapsTo = *op.MapsTo
		case ColumnReorder:
			if op.Position == nil {
				return BoardChange{}, invalid("position is required")
			}
			col := cols[idx]
			cols = slices.Delete(cols, idx, idx+1)
			at := max(0, min(*op.Position, len(cols)))
			cols = slices.Insert(cols, at, col)
		case ColumnRemove:
			if len(cols) <= MinBoardColumns {
				return BoardChange{}, invalid("a board keeps at least %d columns", MinBoardColumns)
			}
			cols = slices.Delete(cols, idx, idx+1)
		case ColumnSetWIP:
			cols[idx].WIPLimit = op.WIPLimit
		case ColumnCollapse:
			if op.Collapsed == nil {
				return BoardChange{}, invalid("collapsed is required")
			}
			cols[idx].Collapsed = *op.Collapsed
		default:
			return BoardChange{}, invalid("unknown column operation")
		}
		for i := range cols {
			cols[i].Position = i
		}
		next := cur
		next.Columns = cols
		meta["columns"] = len(cols)
		return BoardChange{Next: next, Action: "tasks.board.column_changed", Metadata: meta}, nil
	}); err != nil {
		return BoardView{}, err
	}
	return b.Get(ctx, vc, p, id)
}

// Archive archives a Board and its View (owner only; shares stop working).
func (b *Boards) Archive(ctx context.Context, c Caller, vc views.Caller, p Principal, id string, expected int) (BoardView, error) {
	return b.setArchived(ctx, c, vc, p, id, expected, true)
}

// Restore restores an archived Board (owner only).
func (b *Boards) Restore(ctx context.Context, c Caller, vc views.Caller, p Principal, id string, expected int) (BoardView, error) {
	return b.setArchived(ctx, c, vc, p, id, expected, false)
}

func (b *Boards) setArchived(ctx context.Context, c Caller, vc views.Caller, p Principal, id string, expected int, archive bool) (BoardView, error) {
	if err := c.validate(); err != nil {
		return BoardView{}, err
	}
	if expected < 1 {
		return BoardView{}, invalid("expectedVersion is required")
	}
	a, err := b.svc.access(ctx, p)
	if err != nil {
		return BoardView{}, err
	}
	ld, err := b.load(ctx, vc, p, a, id)
	if err != nil {
		return BoardView{}, err
	}
	if ld.res.Access != views.AccessOwner {
		return BoardView{}, ErrForbidden
	}
	if ld.Version != expected {
		return BoardView{}, ErrBoardConflict
	}
	if archive == (ld.ArchivedAt != nil) {
		return BoardView{}, invalid("the board is already in that state")
	}
	if archive {
		_, err = b.views.Archive(ctx, vc, ld.ViewID, ld.res.Version)
	} else {
		_, err = b.views.Restore(ctx, vc, ld.ViewID, ld.res.Version)
	}
	if err != nil {
		return BoardView{}, err
	}
	if _, err := b.store.SetArchived(ctx, c, id, expected, archive); err != nil {
		return BoardView{}, err
	}
	return b.Get(ctx, vc, p, id)
}
