package application

import (
	"context"
	"fmt"
	"strings"

	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/views"
)

// Cards, card moves and rank placement of a Board.
//
// The cards of a column are the tasks that match the Board's View filter (degraded to the viewer's own catalog, as
// for any shared View), whose status is the column's mapping, and that the viewer may see (the task visibility
// predicate is ANDed outside the filter). A viewer therefore sees neither card nor count of a task they may not
// see. Cards are ordered rank first (ranked cards by rank), then the unranked ones in the View's own order.

// boardQuery is everything needed to query the cards of one Board for one caller.
type boardQuery struct {
	ld       loaded
	a        access
	filter   query.Filter
	empty    bool
	warnings []query.Warning
	vis      query.Fragment
	scope    string
}

func (b *Boards) prepare(ld loaded, a access) boardQuery {
	q := boardQuery{ld: ld, a: a, filter: query.Filter{V: 1}}
	q.vis, q.scope = a.visibility(false)
	if ld.Filter != nil {
		deg, warnings, empty := views.Degrade(*ld.Filter, taskCatalog.Describe(a.subject()))
		q.filter, q.warnings, q.empty = deg, warnings, empty
	}
	return q
}

func (q boardQuery) request(col *Column, cursor string, limit int, count bool) query.Request {
	f := q.filter
	fp := &f
	if col != nil {
		fp = query.And(fp, query.Cond("status", query.OpEquals, col.MapsTo))
	}
	return query.Request{Filter: fp, Limit: limit, Cursor: cursor, Count: count}
}

func andFragments(a, b query.Fragment) query.Fragment {
	if a.SQL == "" {
		return b
	}
	return query.Fragment{SQL: "(" + a.SQL + ") AND (" + b.SQL + ")", Args: append(append([]any{}, a.Args...), b.Args...)}
}

// ColumnCards is one page of the cards of a column with the column's visible count.
type ColumnCards struct {
	Column     Column
	Items      []Card
	NextCursor string
	// Count is the number of cards the viewer sees in the column, capped at query.CountCap (CountCapped).
	Count       int
	CountCapped bool
	// OverWIP is true when the count exceeds the column's soft WIP limit.
	OverWIP  bool
	Warnings []query.Warning
}

func (b *Boards) columnPage(ctx context.Context, q boardQuery, col Column, cursor string, limit int, count, rateLimited bool) (ColumnCards, error) {
	out := ColumnCards{Column: col, Items: []Card{}, Warnings: q.warnings}
	if q.empty {
		return out, nil
	}
	first, _ := q.ld.firstColumnOf(col.MapsTo)
	place := &CardPlacement{BoardID: q.ld.ID, ColumnID: col.ID, First: first.ID == col.ID}
	scope := "board:" + q.ld.ID + ":" + col.ID + ":" + q.scope
	plan, err := b.svc.engine.Prepare(taskCatalog, q.a.subject(), q.request(&col, cursor, limit, count), scope,
		query.Options{Lead: &query.LeadKey{Expr: "r.rank"}, RateLimited: rateLimited})
	if err != nil {
		return ColumnCards{}, err
	}
	page, err := b.store.QueryBoardCards(ctx, plan, q.vis, place)
	if err != nil {
		return ColumnCards{}, fmt.Errorf("query board cards: %w", err)
	}
	tasks := make([]Task, len(page.Items))
	for i, c := range page.Items {
		tasks[i] = c.Task
	}
	views, err := b.svc.view(ctx, tasks)
	if err != nil {
		return ColumnCards{}, err
	}
	for i, c := range page.Items {
		out.Items = append(out.Items, Card{TaskView: views[i], Rank: c.Rank})
	}
	out.NextCursor = page.NextCursor
	out.Warnings = append(out.Warnings, page.Warnings...)
	if page.Count != nil {
		out.Count, out.CountCapped = *page.Count, page.CountCapped
		out.OverWIP = col.WIPLimit != nil && out.Count > *col.WIPLimit
	}
	return out, nil
}

// Cards returns the first page of every column (columnID empty) or one page of one column (with cursor), each with
// the visible count. limit is the page size per column.
func (b *Boards) Cards(ctx context.Context, vc views.Caller, p Principal, boardID, columnID, cursor string, limit int) ([]ColumnCards, error) {
	a, err := b.svc.access(ctx, p)
	if err != nil {
		return nil, err
	}
	ld, err := b.load(ctx, vc, p, a, boardID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxCardsPerColumn)
	if ld.res.ArchivedAt != nil {
		return nil, ErrBoardArchived
	}
	q := b.prepare(ld, a)
	if columnID != "" {
		col, ok := ld.column(columnID)
		if !ok {
			return nil, invalid("unknown column")
		}
		cc, err := b.columnPage(ctx, q, col, cursor, limit, true, false)
		if err != nil {
			return nil, err
		}
		return []ColumnCards{cc}, nil
	}
	if cursor != "" {
		return nil, invalid("a cursor needs a column")
	}
	// One board load is one rate-limit token, however many columns it has.
	if err := b.svc.engine.Take(a.subject()); err != nil {
		return nil, err
	}
	out := make([]ColumnCards, 0, len(ld.Columns))
	for _, col := range ld.Columns {
		cc, err := b.columnPage(ctx, q, col, "", limit, true, true)
		if err != nil {
			return nil, err
		}
		out = append(out, cc)
	}
	return out, nil
}

// members returns which of the task ids are cards of the Board for the caller: visible to the caller and matching
// the Board's filter, in any status.
func (b *Boards) members(ctx context.Context, q boardQuery, ids []string) (map[string]bool, error) {
	out := map[string]bool{}
	if q.empty || len(ids) == 0 {
		return out, nil
	}
	for _, id := range ids {
		if !isUUID(id) {
			return out, nil
		}
	}
	plan, err := b.svc.engine.Prepare(taskCatalog, q.a.subject(), q.request(nil, "", len(ids), false), "board-members:"+q.ld.ID+":"+q.scope, query.Options{})
	if err != nil {
		return nil, err
	}
	vis := andFragments(q.vis, query.Fragment{SQL: "t.id = ANY(?::text[]::uuid[])", Args: []any{ids}})
	page, err := b.store.QueryBoardCards(ctx, plan, vis, nil)
	if err != nil {
		return nil, fmt.Errorf("query board members: %w", err)
	}
	for _, c := range page.Items {
		out[c.ID] = true
	}
	return out, nil
}

// ---- moves ----

// moveOperation returns the Task lifecycle operation that leads from a status to the target status.
func moveOperation(from, to string) (Operation, bool) {
	for _, op := range []Operation{OpStart, OpBlock, OpUnblock, OpComplete, OpCancel, OpReopen} {
		tr := transitions[op]
		if tr.to == to && contains(tr.from, from) {
			return op, true
		}
	}
	return "", false
}

// MoveInput moves one card to a column.
type MoveInput struct {
	TaskID string
	// ExpectedVersion is the task version the card was shown with; it is mandatory.
	ExpectedVersion *int
	ColumnID        string
	// Reason is required for the moves that block, cancel or reopen a task (as for the Task operations).
	Reason string
	// AfterTaskID places the card below that card of the target column; AtTop places it first. Without either the
	// card is not placed explicitly (it joins the unranked cards), which needs no edit access to the Board.
	AfterTaskID string
	AtTop       bool
}

// MoveResult is the outcome of a card move.
type MoveResult struct {
	Task TaskView
	// Operation is the Task operation that was executed; empty for a placement within the same status.
	Operation string
	// Placed reports whether the explicit placement was written.
	Placed bool
	Rank   *string
}

// Move moves a card. A different status runs the matching Task lifecycle operation with the mandatory expected
// version and the task's own authorization (tasks.manage, or tasks.work on a task assigned to the caller or its
// Teams); a transition the Task state machine forbids is rejected unchanged. The same status only places the card
// (Board edit access). Placing a card explicitly, or into a column that is not the first of its status, needs
// Board edit access as well.
func (b *Boards) Move(ctx context.Context, c Caller, vc views.Caller, p Principal, boardID string, in MoveInput) (MoveResult, error) {
	if err := c.validate(); err != nil {
		return MoveResult{}, err
	}
	if in.ExpectedVersion == nil {
		return MoveResult{}, invalid("expectedVersion is required to move a card")
	}
	if !isUUID(in.TaskID) || !isUUID(in.ColumnID) || (in.AfterTaskID != "" && !isUUID(in.AfterTaskID)) {
		return MoveResult{}, invalid("taskId and columnId must be ids")
	}
	if in.AfterTaskID != "" && in.AtTop {
		return MoveResult{}, invalid("afterTaskId and top exclude each other")
	}
	if in.AfterTaskID == in.TaskID {
		return MoveResult{}, invalid("a card cannot be placed after itself")
	}
	a, err := b.svc.access(ctx, p)
	if err != nil {
		return MoveResult{}, err
	}
	ld, err := b.load(ctx, vc, p, a, boardID)
	if err != nil {
		return MoveResult{}, err
	}
	if ld.ArchivedAt != nil || ld.res.ArchivedAt != nil {
		return MoveResult{}, ErrBoardArchived
	}
	col, ok := ld.column(in.ColumnID)
	if !ok {
		return MoveResult{}, invalid("unknown column")
	}
	q := b.prepare(ld, a)
	mem, err := b.members(ctx, q, []string{in.TaskID})
	if err != nil {
		return MoveResult{}, err
	}
	if !mem[in.TaskID] {
		return MoveResult{}, ErrNotFound
	}
	cur, err := b.svc.store.Get(ctx, in.TaskID)
	if err != nil {
		return MoveResult{}, err
	}
	first, _ := ld.firstColumnOf(col.MapsTo)
	place := in.AfterTaskID != "" || in.AtTop || first.ID != col.ID
	if place && !ld.CanEdit {
		return MoveResult{}, ErrForbidden
	}
	atTop := in.AtTop || (in.AfterTaskID == "" && place)
	res := MoveResult{}
	if cur.Status == col.MapsTo {
		// Same status: presentation only.
		if *in.ExpectedVersion != cur.Version {
			return MoveResult{}, ErrVersionConflict
		}
		if !place {
			// Dropped on the first column of its status: an earlier placement in another column of the status ends.
			_ = b.store.ClearPlacement(ctx, boardID, in.TaskID)
			v, err := b.svc.viewOne(ctx, cur)
			return MoveResult{Task: v}, err
		}
		rank, err := b.place(ctx, vc, p, a, boardID, in.TaskID, col.ID, in.AfterTaskID, atTop)
		if err != nil {
			return MoveResult{}, err
		}
		v, err := b.svc.viewOne(ctx, cur)
		return MoveResult{Task: v, Placed: true, Rank: &rank}, err
	}
	op, ok := moveOperation(cur.Status, col.MapsTo)
	if !ok {
		return MoveResult{}, &InvalidTransitionError{Operation: "move", From: cur.Status}
	}
	if transitions[op].reason && strings.TrimSpace(in.Reason) == "" {
		return MoveResult{}, invalid("a reason is required for this move")
	}
	v, err := b.svc.Transition(ctx, c, p, in.TaskID, in.ExpectedVersion, op, in.Reason)
	if err != nil {
		return MoveResult{}, err
	}
	res.Task, res.Operation = v, string(op)
	if place {
		rank, err := b.place(ctx, vc, p, a, boardID, in.TaskID, col.ID, in.AfterTaskID, atTop)
		if err == nil {
			res.Placed, res.Rank = true, &rank
		}
		// The task has moved; a failed placement leaves it among the unranked cards of the column.
		return res, nil
	}
	// The old placement refers to the previous status; drop it so the card joins the first column of its status.
	_ = b.store.ClearPlacement(ctx, boardID, in.TaskID)
	return res, nil
}

// ---- ranks ----

// PlaceInput places a card within a column.
type PlaceInput struct {
	TaskID      string
	ColumnID    string
	AfterTaskID string
	AtTop       bool
}

// Place orders a card within a column of the same status (PUT /ranks). It needs Board edit access, re-checked under
// the Board's rank lock, and writes only presentation data; the task is not changed.
func (b *Boards) Place(ctx context.Context, vc views.Caller, p Principal, boardID string, in PlaceInput) (string, error) {
	if !isUUID(in.TaskID) || !isUUID(in.ColumnID) || (in.AfterTaskID != "" && !isUUID(in.AfterTaskID)) {
		return "", invalid("taskId and columnId must be ids")
	}
	if (in.AfterTaskID == "") == !in.AtTop {
		return "", invalid("exactly one of afterTaskId and top is required")
	}
	if in.AfterTaskID == in.TaskID {
		return "", invalid("a card cannot be placed after itself")
	}
	a, err := b.svc.access(ctx, p)
	if err != nil {
		return "", err
	}
	return b.place(ctx, vc, p, a, boardID, in.TaskID, in.ColumnID, in.AfterTaskID, in.AtTop)
}

func (b *Boards) place(ctx context.Context, vc views.Caller, p Principal, a access, boardID, taskID, columnID, afterID string, atTop bool) (string, error) {
	// Ranks that grew too long are rebalanced in a transaction of their own; the placement is then computed again
	// from the committed ranks.
	for attempt := 0; attempt < 3; attempt++ {
		var rank string
		rebalanced := false
		release, err := b.locks.acquire(ctx, boardID)
		if err != nil {
			return "", err
		}
		err = b.store.WithRankLock(ctx, boardID, func(tx RankTx) error {
			// Access is decided under the lock: a revoked share or a changed column set stops the write.
			ld, err := b.requireEdit(ctx, vc, p, a, boardID)
			if err != nil {
				return err
			}
			col, ok := ld.column(columnID)
			if !ok {
				return invalid("unknown column")
			}
			q := b.prepare(ld, a)
			ids := []string{taskID}
			if afterID != "" {
				ids = append(ids, afterID)
			}
			mem, err := b.members(ctx, q, ids)
			if err != nil {
				return err
			}
			if !mem[taskID] {
				return ErrNotFound
			}
			if afterID != "" && !mem[afterID] {
				return ErrAnchorInvalid
			}
			cur, err := b.svc.store.Get(ctx, taskID)
			if err != nil {
				return err
			}
			if cur.Status != col.MapsTo {
				return invalid("the card's status does not match the column; move the card instead")
			}
			r, rebalance, err := b.computeRank(ctx, tx, q, col, taskID, afterID, atTop)
			if err != nil {
				return err
			}
			if rebalance {
				rebalanced = true
				return tx.Rebalance(ctx)
			}
			rank = r
			return tx.Put(ctx, taskID, r, col.ID)
		})
		release()
		if err != nil {
			return "", err
		}
		if !rebalanced {
			return rank, nil
		}
	}
	return "", ErrBoardLimit
}

// computeRank decides the rank of the moved card inside the locked transaction. Unranked cards above the anchor are
// ranked first (in their current order, after every existing rank) so the moved card can sit right below the
// anchor. rebalance is true when ranks grew too long and the Board must be rebalanced first.
func (b *Boards) computeRank(ctx context.Context, tx RankTx, q boardQuery, col Column, taskID, afterID string, atTop bool) (rank string, rebalance bool, err error) {
	var cards []Card
	cursor := ""
	for len(cards) < MaxCardsPerColumn {
		win, err := b.columnPage(ctx, q, col, cursor, query.MaxPageSize, false, false)
		if err != nil {
			return "", false, err
		}
		for _, c := range win.Items {
			if c.ID != taskID {
				cards = append(cards, c)
			}
		}
		if win.NextCursor == "" {
			break
		}
		cursor = win.NextCursor
	}
	if len(cards) > MaxCardsPerColumn {
		cards = cards[:MaxCardsPerColumn]
	}
	if atTop {
		lower, upper := "", ""
		if len(cards) > 0 && cards[0].Rank != nil {
			upper = *cards[0].Rank
			if lower, _, err = tx.Predecessor(ctx, upper); err != nil {
				return "", false, err
			}
		} else if upper, _, err = tx.Min(ctx); err != nil {
			return "", false, err
		}
		if err := b.reserve(ctx, tx, 1); err != nil {
			return "", false, err
		}
		r, err := rankBetween(lower, upper)
		return r, len(r) > maxRankLength, err
	}
	idx := -1
	need := 1
	for i, c := range cards {
		if c.ID == afterID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return "", false, ErrAnchorInvalid
	}
	for _, c := range cards[:idx+1] {
		if c.Rank == nil {
			need++
		}
	}
	if err := b.reserve(ctx, tx, need); err != nil {
		return "", false, err
	}
	highest, _, err := tx.Max(ctx)
	if err != nil {
		return "", false, err
	}
	var unranked []int
	for i := 0; i <= idx; i++ {
		if cards[i].Rank == nil {
			unranked = append(unranked, i)
		}
	}
	if len(unranked) > 0 {
		run := ranksAfter(highest, len(unranked))
		if len(run[len(run)-1]) > maxRankLength {
			return "", true, nil
		}
		for k, i := range unranked {
			if err := tx.Put(ctx, cards[i].ID, run[k], ""); err != nil {
				return "", false, err
			}
			r := run[k]
			cards[i].Rank = &r
		}
	}
	anchor := *cards[idx].Rank
	upper, _, err := tx.Successor(ctx, anchor)
	if err != nil {
		return "", false, err
	}
	r, err := rankBetween(anchor, upper)
	return r, len(r) > maxRankLength, err
}

// reserve makes room for n more ranks of the Board: ranks of finished tasks are pruned before the limit is hit.
func (b *Boards) reserve(ctx context.Context, tx RankTx, n int) error {
	count, err := tx.Count(ctx)
	if err != nil {
		return err
	}
	if count+n <= MaxRanksPerBoard {
		return nil
	}
	if _, err := tx.PruneTerminal(ctx); err != nil {
		return err
	}
	if count, err = tx.Count(ctx); err != nil {
		return err
	}
	if count+n > MaxRanksPerBoard {
		return ErrBoardLimit
	}
	return nil
}
