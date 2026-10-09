package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/MagicalWig34653/turaco/backend/internal/modules/tasks/application"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/audit"
	"github.com/MagicalWig34653/turaco/backend/internal/platform/query"
)

var _ application.BoardStore = (*Repository)(nil)

const boardTarget = "task_board"

const boardColumns = `id::text, view_id::text, created_by::text, owner_team_id::text, swimlane, version, archived_at, created_at, updated_at`

func scanBoard(row pgx.Row) (application.Board, error) {
	var b application.Board
	err := row.Scan(&b.ID, &b.ViewID, &b.CreatedBy, &b.OwnerTeamID, &b.Swimlane, &b.Version, &b.ArchivedAt, &b.CreatedAt, &b.UpdatedAt)
	return b, err
}

type boardQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// loadColumns attaches the ordered columns to the Boards.
func loadColumns(ctx context.Context, q boardQuerier, boards []application.Board) error {
	if len(boards) == 0 {
		return nil
	}
	ids := make([]string, len(boards))
	index := make(map[string]int, len(boards))
	for i, b := range boards {
		ids[i] = b.ID
		index[b.ID] = i
	}
	rows, err := q.Query(ctx, `
		SELECT board_id::text, id::text, position, title, maps_to, collapsed, wip_limit
		FROM tasks.board_columns WHERE board_id = ANY($1::text[]::uuid[]) ORDER BY board_id, position`, ids)
	if err != nil {
		return fmt.Errorf("load board columns: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var boardID string
		var c application.Column
		if err := rows.Scan(&boardID, &c.ID, &c.Position, &c.Title, &c.MapsTo, &c.Collapsed, &c.WIPLimit); err != nil {
			return fmt.Errorf("load board columns: scan: %w", err)
		}
		boards[index[boardID]].Columns = append(boards[index[boardID]].Columns, c)
	}
	return rows.Err()
}

func recordBoard(ctx context.Context, tx pgx.Tx, c application.Caller, action, id string, meta map[string]any) error {
	return audit.Record(ctx, tx, audit.Change{Action: action, TargetType: boardTarget, TargetID: id, Actor: c.Actor,
		CorrelationID: c.CorrelationID, Metadata: meta})
}

func (r *Repository) CreateBoard(ctx context.Context, c application.Caller, n application.NewBoard) (application.Board, error) {
	var out application.Board
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		// Serialize creations of one creator (and Team) so the per-owner limit cannot be passed concurrently.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('tasks.boards.owner:' || $1, 0))`, n.CreatedBy); err != nil {
			return fmt.Errorf("lock board owner: %w", err)
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tasks.boards WHERE created_by = $1::uuid AND archived_at IS NULL`, n.CreatedBy).Scan(&count); err != nil {
			return fmt.Errorf("count boards: %w", err)
		}
		if count >= application.MaxBoardsPerOwner {
			return application.ErrBoardLimit
		}
		if n.OwnerTeamID != nil {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('tasks.boards.team:' || $1, 0))`, *n.OwnerTeamID); err != nil {
				return fmt.Errorf("lock board team: %w", err)
			}
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM tasks.boards WHERE owner_team_id = $1::uuid AND archived_at IS NULL`, *n.OwnerTeamID).Scan(&count); err != nil {
				return fmt.Errorf("count team boards: %w", err)
			}
			if count >= application.MaxBoardsPerOwner {
				return application.ErrBoardLimit
			}
		}
		b, err := scanBoard(tx.QueryRow(ctx, `
			INSERT INTO tasks.boards (view_id, created_by, owner_team_id, swimlane)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4) RETURNING `+boardColumns, n.ViewID, n.CreatedBy, n.OwnerTeamID, n.Swimlane))
		if err != nil {
			return fmt.Errorf("insert board: %w", err)
		}
		for i, col := range n.Columns {
			if _, err := tx.Exec(ctx, `
				INSERT INTO tasks.board_columns (board_id, position, title, maps_to, collapsed, wip_limit)
				VALUES ($1::uuid, $2, $3, $4, $5, $6)`, b.ID, i, col.Title, col.MapsTo, col.Collapsed, col.WIPLimit); err != nil {
				return fmt.Errorf("insert board column: %w", err)
			}
		}
		boards := []application.Board{b}
		if err := loadColumns(ctx, tx, boards); err != nil {
			return err
		}
		out = boards[0]
		meta := map[string]any{"viewId": out.ViewID, "columns": len(out.Columns), "swimlane": out.Swimlane}
		if out.OwnerTeamID != nil {
			meta["teamId"] = *out.OwnerTeamID
		}
		if err := recordBoard(ctx, tx, c, "tasks.board.created", out.ID, meta); err != nil {
			return err
		}
		return publish(ctx, tx, c, application.Event{Type: "TaskBoardCreated", Payload: map[string]any{"boardId": out.ID, "viewId": out.ViewID}})
	})
	if err != nil {
		return application.Board{}, err
	}
	return out, nil
}

func (r *Repository) GetBoard(ctx context.Context, id string) (application.Board, error) {
	if !validUUID(id) {
		return application.Board{}, application.ErrBoardNotFound
	}
	b, err := scanBoard(r.pool.QueryRow(ctx, `SELECT `+boardColumns+` FROM tasks.boards WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Board{}, application.ErrBoardNotFound
	}
	if err != nil {
		return application.Board{}, fmt.Errorf("get board: %w", err)
	}
	boards := []application.Board{b}
	if err := loadColumns(ctx, r.pool, boards); err != nil {
		return application.Board{}, err
	}
	return boards[0], nil
}

func (r *Repository) ListBoards(ctx context.Context, afterID string, limit int) ([]application.Board, error) {
	if afterID != "" && !validUUID(afterID) {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+boardColumns+` FROM tasks.boards
		WHERE archived_at IS NULL AND ($1 = '' OR id < NULLIF($1, '')::uuid) ORDER BY id DESC LIMIT $2`, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list boards: %w", err)
	}
	defer rows.Close()
	var out []application.Board
	for rows.Next() {
		b, err := scanBoard(rows)
		if err != nil {
			return nil, fmt.Errorf("list boards: scan: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list boards: %w", err)
	}
	rows.Close()
	return out, loadColumns(ctx, r.pool, out)
}

func (r *Repository) ChangeBoard(ctx context.Context, c application.Caller, id string, decide func(cur application.Board) (application.BoardChange, error)) (application.Board, error) {
	if !validUUID(id) {
		return application.Board{}, application.ErrBoardNotFound
	}
	var out application.Board
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		cur, err := scanBoard(tx.QueryRow(ctx, `SELECT `+boardColumns+` FROM tasks.boards WHERE id = $1::uuid FOR UPDATE`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrBoardNotFound
		}
		if err != nil {
			return fmt.Errorf("lock board: %w", err)
		}
		boards := []application.Board{cur}
		if err := loadColumns(ctx, tx, boards); err != nil {
			return err
		}
		cur = boards[0]
		change, err := decide(cur)
		if err != nil {
			return err
		}
		if change.NoChange {
			out = cur
			return nil
		}
		next := change.Next
		if _, err := tx.Exec(ctx, `UPDATE tasks.boards SET swimlane = $2, version = version + 1, updated_at = now() WHERE id = $1::uuid`, id, next.Swimlane); err != nil {
			return fmt.Errorf("update board: %w", err)
		}
		keep := make([]string, 0, len(next.Columns))
		for _, col := range next.Columns {
			if col.ID != "" {
				keep = append(keep, col.ID)
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM tasks.board_columns WHERE board_id = $1::uuid AND NOT (id = ANY($2::text[]::uuid[]))`, id, keep); err != nil {
			return fmt.Errorf("delete board columns: %w", err)
		}
		for i, col := range next.Columns {
			if col.ID == "" {
				_, err = tx.Exec(ctx, `
					INSERT INTO tasks.board_columns (board_id, position, title, maps_to, collapsed, wip_limit)
					VALUES ($1::uuid, $2, $3, $4, $5, $6)`, id, i, col.Title, col.MapsTo, col.Collapsed, col.WIPLimit)
			} else {
				_, err = tx.Exec(ctx, `
					UPDATE tasks.board_columns SET position = $3, title = $4, maps_to = $5, collapsed = $6, wip_limit = $7
					WHERE board_id = $1::uuid AND id = $2::uuid`, id, col.ID, i, col.Title, col.MapsTo, col.Collapsed, col.WIPLimit)
			}
			if err != nil {
				return fmt.Errorf("write board column: %w", err)
			}
		}
		updated := []application.Board{{ID: id}}
		b, err := scanBoard(tx.QueryRow(ctx, `SELECT `+boardColumns+` FROM tasks.boards WHERE id = $1::uuid`, id))
		if err != nil {
			return fmt.Errorf("reload board: %w", err)
		}
		updated[0] = b
		if err := loadColumns(ctx, tx, updated); err != nil {
			return err
		}
		out = updated[0]
		meta := map[string]any{"version": out.Version}
		for k, v := range change.Metadata {
			meta[k] = v
		}
		if err := recordBoard(ctx, tx, c, change.Action, id, meta); err != nil {
			return err
		}
		for _, e := range change.Events {
			if err := publish(ctx, tx, c, e); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return application.Board{}, err
	}
	return out, nil
}

func (r *Repository) SetArchived(ctx context.Context, c application.Caller, id string, expected int, archived bool) (application.Board, error) {
	if !validUUID(id) {
		return application.Board{}, application.ErrBoardNotFound
	}
	action, setSQL := "tasks.board.restored", "archived_at = NULL"
	if archived {
		action, setSQL = "tasks.board.archived", "archived_at = now()"
	}
	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		cur, err := scanBoard(tx.QueryRow(ctx, `SELECT `+boardColumns+` FROM tasks.boards WHERE id = $1::uuid FOR UPDATE`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return application.ErrBoardNotFound
		}
		if err != nil {
			return fmt.Errorf("lock board: %w", err)
		}
		if cur.Version != expected {
			return application.ErrBoardConflict
		}
		if _, err := tx.Exec(ctx, `UPDATE tasks.boards SET `+setSQL+`, version = version + 1, updated_at = now() WHERE id = $1::uuid`, id); err != nil {
			return fmt.Errorf("archive board: %w", err)
		}
		if err := recordBoard(ctx, tx, c, action, id, map[string]any{"viewId": cur.ViewID, "version": cur.Version + 1}); err != nil {
			return err
		}
		if archived {
			return publish(ctx, tx, c, application.Event{Type: "TaskBoardArchived", Payload: map[string]any{"boardId": id, "viewId": cur.ViewID}})
		}
		return nil
	})
	if err != nil {
		return application.Board{}, err
	}
	return r.GetBoard(ctx, id)
}

func (r *Repository) BoardsOfViews(ctx context.Context, viewIDs []string) (map[string]string, error) {
	valid := make([]string, 0, len(viewIDs))
	for _, id := range viewIDs {
		if validUUID(id) {
			valid = append(valid, id)
		}
	}
	rows, err := r.pool.Query(ctx, `SELECT view_id::text, id::text FROM tasks.boards WHERE view_id = ANY($1::text[]::uuid[]) AND archived_at IS NULL`, valid)
	if err != nil {
		return nil, fmt.Errorf("boards of views: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var viewID, boardID string
		if err := rows.Scan(&viewID, &boardID); err != nil {
			return nil, fmt.Errorf("boards of views: scan: %w", err)
		}
		out[viewID] = boardID
	}
	return out, rows.Err()
}

// placementFragment keeps the cards that belong to the column: those placed in it, and, for the first column of a
// status, those without a placement or whose placement no longer maps to their status.
func placementFragment(p application.CardPlacement) query.Fragment {
	first := "FALSE"
	if p.First {
		first = "TRUE"
	}
	return query.Fragment{
		SQL: `r.column_id = ?::uuid OR (` + first + ` AND (r.column_id IS NULL OR NOT EXISTS (
			SELECT 1 FROM tasks.board_columns c2 WHERE c2.board_id = r.board_id AND c2.id = r.column_id AND c2.maps_to = t.status)))`,
		Args: []any{p.ColumnID},
	}
}

func (r *Repository) QueryBoardCards(ctx context.Context, plan *query.Plan, vis query.Fragment, place *application.CardPlacement) (query.Page[application.Card], error) {
	sel := query.Select{Columns: queryColumns, Visibility: vis}
	if place != nil {
		sel.Join = query.Fragment{SQL: `LEFT JOIN tasks.board_card_ranks r ON r.board_id = ?::uuid AND r.task_id = t.id`, Args: []any{place.BoardID}}
		pf := placementFragment(*place)
		if vis.SQL == "" {
			sel.Visibility = pf
		} else {
			sel.Visibility = query.Fragment{SQL: "(" + vis.SQL + ") AND (" + pf.SQL + ")", Args: append(append([]any{}, vis.Args...), pf.Args...)}
		}
	}
	return query.Run(ctx, r.pool, plan, sel, func(rows pgx.Rows, extra []any) (application.Card, error) {
		t, err := scanWith(rows, extra...)
		if err != nil {
			return application.Card{}, err
		}
		card := application.Card{TaskView: application.TaskView{Task: t}}
		if place != nil && len(extra) > 0 {
			// The leading sort key is the rank.
			if rank, ok := extra[0].(**string); ok {
				card.Rank = *rank
			}
		}
		return card, nil
	})
}

func (r *Repository) ClearPlacement(ctx context.Context, boardID, taskID string) error {
	if !validUUID(boardID) || !validUUID(taskID) {
		return nil
	}
	if _, err := r.pool.Exec(ctx, `UPDATE tasks.board_card_ranks SET column_id = NULL, updated_at = now()
		WHERE board_id = $1::uuid AND task_id = $2::uuid AND column_id IS NOT NULL`, boardID, taskID); err != nil {
		return fmt.Errorf("clear placement: %w", err)
	}
	return nil
}

// WithRankLock runs fn inside a transaction that holds the Board's advisory rank lock.
func (r *Repository) WithRankLock(ctx context.Context, boardID string, fn func(tx application.RankTx) error) error {
	if !validUUID(boardID) {
		return application.ErrBoardNotFound
	}
	return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('tasks.board.ranks:' || $1, 0))`, boardID); err != nil {
			return fmt.Errorf("lock board ranks: %w", err)
		}
		return fn(&rankTx{tx: tx, boardID: boardID})
	})
}

type rankTx struct {
	tx      pgx.Tx
	boardID string
}

func (t *rankTx) one(ctx context.Context, sql string, args ...any) (string, bool, error) {
	var v *string
	if err := t.tx.QueryRow(ctx, sql, append([]any{t.boardID}, args...)...).Scan(&v); err != nil {
		return "", false, fmt.Errorf("read rank: %w", err)
	}
	if v == nil {
		return "", false, nil
	}
	return *v, true, nil
}

func (t *rankTx) Max(ctx context.Context) (string, bool, error) {
	return t.one(ctx, `SELECT max(rank) FROM tasks.board_card_ranks WHERE board_id = $1::uuid`)
}

func (t *rankTx) Min(ctx context.Context) (string, bool, error) {
	return t.one(ctx, `SELECT min(rank) FROM tasks.board_card_ranks WHERE board_id = $1::uuid`)
}

func (t *rankTx) Successor(ctx context.Context, rank string) (string, bool, error) {
	return t.one(ctx, `SELECT min(rank) FROM tasks.board_card_ranks WHERE board_id = $1::uuid AND rank > $2::text COLLATE "C"`, rank)
}

func (t *rankTx) Predecessor(ctx context.Context, rank string) (string, bool, error) {
	return t.one(ctx, `SELECT max(rank) FROM tasks.board_card_ranks WHERE board_id = $1::uuid AND rank < $2::text COLLATE "C"`, rank)
}

func (t *rankTx) Put(ctx context.Context, taskID, rank, columnID string) error {
	if _, err := t.tx.Exec(ctx, `
		INSERT INTO tasks.board_card_ranks (board_id, task_id, rank, column_id)
		VALUES ($1::uuid, $2::uuid, $3, NULLIF($4, '')::uuid)
		ON CONFLICT (board_id, task_id) DO UPDATE SET rank = EXCLUDED.rank, column_id = EXCLUDED.column_id, updated_at = now()`,
		t.boardID, taskID, rank, columnID); err != nil {
		return fmt.Errorf("write rank: %w", err)
	}
	return nil
}

func (t *rankTx) Count(ctx context.Context) (int, error) {
	var n int
	if err := t.tx.QueryRow(ctx, `SELECT count(*) FROM tasks.board_card_ranks WHERE board_id = $1::uuid`, t.boardID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count ranks: %w", err)
	}
	return n, nil
}

func (t *rankTx) PruneTerminal(ctx context.Context) (int, error) {
	tag, err := t.tx.Exec(ctx, `
		DELETE FROM tasks.board_card_ranks r USING platform.tasks k
		WHERE r.board_id = $1::uuid AND k.id = r.task_id AND k.status IN ('completed', 'cancelled')`, t.boardID)
	if err != nil {
		return 0, fmt.Errorf("prune ranks: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (t *rankTx) Rebalance(ctx context.Context) error {
	rows, err := t.tx.Query(ctx, `SELECT task_id::text FROM tasks.board_card_ranks WHERE board_id = $1::uuid ORDER BY rank, task_id`, t.boardID)
	if err != nil {
		return fmt.Errorf("rebalance ranks: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("rebalance ranks: scan: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rebalance ranks: %w", err)
	}
	ranks := make([]string, len(ids))
	for i := range ids {
		ranks[i] = application.SpreadRank(i, len(ids))
	}
	// The unique rank constraint is deferred, so one statement may reassign every rank.
	if _, err := t.tx.Exec(ctx, `
		UPDATE tasks.board_card_ranks r SET rank = u.rank, updated_at = now()
		FROM unnest($2::text[]::uuid[], $3::text[]) AS u(task_id, rank)
		WHERE r.board_id = $1::uuid AND r.task_id = u.task_id`, t.boardID, ids, ranks); err != nil {
		return fmt.Errorf("rebalance ranks: %w", err)
	}
	return nil
}
