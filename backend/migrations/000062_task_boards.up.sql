-- Task Boards (ADR-0033, F13 slice Q-D, docs/product/f13-workbench-views-design.md). Owned by the tasks module.
--
-- A Board is a Saved View over tasks (the View carries name, filter, sort and sharing: one sharing mechanism, see
-- platform/views) plus ordered Columns that map to Task statuses and per-board card ranks. A Board never stores a
-- Task status and never moves a Task: a card move is a Task lifecycle operation (start, block, ...) executed by the
-- tasks module. Ranks and column placement are presentation data of the Board.

CREATE SCHEMA IF NOT EXISTS tasks;

CREATE TABLE IF NOT EXISTS tasks.boards (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    -- The Saved View (resource 'tasks') that selects the cards, name and shares of this Board. Purging an archived
    -- View removes its Board.
    view_id uuid NOT NULL UNIQUE REFERENCES views.saved_views(id) ON DELETE CASCADE,
    -- The User who created the Board (counts against the per-creator limit); the View owner may change by take-over.
    created_by uuid NOT NULL,
    -- Team-owned Boards are shown to the Team's members (a Team share on the View) and edited by holders of
    -- tasks.boards.manage_team; by id, no foreign key across modules.
    owner_team_id uuid,
    swimlane text NOT NULL DEFAULT 'none' CHECK (swimlane IN ('none', 'assignee')),
    version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
    archived_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS boards_created_by_idx ON tasks.boards (created_by) WHERE archived_at IS NULL;
CREATE INDEX IF NOT EXISTS boards_owner_team_idx ON tasks.boards (owner_team_id) WHERE archived_at IS NULL AND owner_team_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS boards_active_idx ON tasks.boards (id DESC) WHERE archived_at IS NULL;

CREATE TABLE IF NOT EXISTS tasks.board_columns (
    board_id uuid NOT NULL REFERENCES tasks.boards(id) ON DELETE CASCADE,
    id uuid NOT NULL DEFAULT uuidv7(),
    position integer NOT NULL CHECK (position BETWEEN 0 AND 7),
    title text NOT NULL CHECK (title = btrim(title) AND char_length(title) BETWEEN 1 AND 40),
    -- Every column maps to exactly one Task status. Several columns may map to the same status (display grouping);
    -- a card then sits in the column of its rank row, or in the first column of its status.
    maps_to text NOT NULL CHECK (maps_to IN ('open', 'in_progress', 'blocked', 'completed', 'cancelled')),
    collapsed boolean NOT NULL DEFAULT false,
    -- Soft WIP limit: exceeding it is reported, never enforced.
    wip_limit integer CHECK (wip_limit BETWEEN 1 AND 999),
    PRIMARY KEY (board_id, id),
    UNIQUE (board_id, position) DEFERRABLE INITIALLY DEFERRED
);

CREATE TABLE IF NOT EXISTS tasks.board_card_ranks (
    board_id uuid NOT NULL REFERENCES tasks.boards(id) ON DELETE CASCADE,
    task_id uuid NOT NULL REFERENCES platform.tasks(id) ON DELETE CASCADE,
    -- Fractional index (base 62, bytewise order) that orders the cards of a column; cards without a row sort after
    -- ranked cards in the View's own order. It is unique per Board so that the order is total.
    rank text COLLATE "C" NOT NULL CHECK (rank ~ '^[0-9A-Za-z]{1,64}$' AND rank !~ '0$'),
    -- The column the card was placed in; NULL means the first column of the card's status. Removing a column
    -- clears the placement of its cards (they fall back to the first column of their status).
    column_id uuid,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (board_id, task_id),
    -- Deferred so that rebalancing the ranks of a Board in one statement cannot fail on a transient duplicate.
    UNIQUE (board_id, rank) DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY (board_id, column_id) REFERENCES tasks.board_columns(board_id, id) ON DELETE SET NULL (column_id)
);
CREATE INDEX IF NOT EXISTS board_card_ranks_task_idx ON tasks.board_card_ranks (task_id);
