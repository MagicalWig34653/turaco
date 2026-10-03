-- Runbooks (F5 part 2): reusable procedures whose executions become tracked Tasks.
CREATE SEQUENCE IF NOT EXISTS knowledge.runbook_number_seq;

CREATE OR REPLACE FUNCTION knowledge.next_runbook_reference() RETURNS text
LANGUAGE sql
AS $$
    SELECT 'RB-' || CASE WHEN n < 1000000 THEN lpad(n::text, 6, '0') ELSE n::text END
    FROM (SELECT nextval('knowledge.runbook_number_seq') AS n) AS s
$$;

CREATE TABLE IF NOT EXISTS knowledge.runbooks (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    reference text NOT NULL DEFAULT knowledge.next_runbook_reference(),
    title text NOT NULL CHECK (title = btrim(title) AND length(title) BETWEEN 1 AND 200),
    description text NOT NULL DEFAULT '' CHECK (length(description) <= 2000),
    -- Ordered steps: [{"title": ..., "description": ..., "teamId": ...}], validated by the application.
    steps jsonb NOT NULL CHECK (jsonb_typeof(steps) = 'array' AND jsonb_array_length(steps) BETWEEN 1 AND 30),
    active boolean NOT NULL DEFAULT true,
    created_by uuid,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT runbooks_reference_unique UNIQUE (reference)
);

CREATE TABLE IF NOT EXISTS knowledge.runbook_executions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    runbook_id uuid NOT NULL REFERENCES knowledge.runbooks(id),
    -- Title and steps as they were when the execution started.
    runbook_title text NOT NULL,
    steps jsonb NOT NULL,
    context_type text CHECK (context_type IN ('ticket')),
    context_id uuid,
    status text NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'completed', 'cancelled')),
    started_by uuid,
    finished_at timestamptz,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT runbook_executions_context_matches CHECK ((context_type IS NULL) = (context_id IS NULL)),
    CONSTRAINT runbook_executions_finished_matches CHECK ((status = 'running') = (finished_at IS NULL))
);
CREATE INDEX IF NOT EXISTS runbook_executions_runbook_idx ON knowledge.runbook_executions (runbook_id, id DESC);
CREATE INDEX IF NOT EXISTS runbook_executions_context_idx ON knowledge.runbook_executions (context_id) WHERE context_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS runbook_executions_running_idx ON knowledge.runbook_executions (id DESC) WHERE status = 'running';

CREATE TABLE IF NOT EXISTS knowledge.runbook_execution_tasks (
    execution_id uuid NOT NULL REFERENCES knowledge.runbook_executions(id) ON DELETE CASCADE,
    task_id uuid NOT NULL,
    step_index integer NOT NULL CHECK (step_index >= 0),
    PRIMARY KEY (execution_id, task_id),
    CONSTRAINT runbook_execution_tasks_task_unique UNIQUE (task_id)
);
