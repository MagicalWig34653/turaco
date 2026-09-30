CREATE TABLE IF NOT EXISTS platform.tasks (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    title text NOT NULL,
    description text,
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open','in_progress','blocked','completed','cancelled')),
    priority text NOT NULL DEFAULT 'normal' CHECK (priority IN ('low','normal','high','urgent')),
    assigned_user_id uuid,
    assigned_team_id uuid,
    context_type text,
    context_id uuid,
    due_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS tasks_user_work_idx ON platform.tasks(assigned_user_id, status, due_at) WHERE status NOT IN ('completed','cancelled');
CREATE INDEX IF NOT EXISTS tasks_team_work_idx ON platform.tasks(assigned_team_id, status, due_at) WHERE status NOT IN ('completed','cancelled');
