-- Recurring Task Definitions (F2 slice 5). A definition generates real Tasks
-- (docs/domain/core-data-model.md); like platform.tasks it lives in the
-- platform schema, which the tasks module owns.
CREATE TABLE IF NOT EXISTS platform.recurring_task_definitions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    title text NOT NULL CHECK (btrim(title) <> ''),
    description text,
    priority text NOT NULL DEFAULT 'normal' CHECK (priority IN ('low','normal','high','urgent')),
    assigned_user_id uuid,
    assigned_team_id uuid,
    due_after_hours integer CHECK (due_after_hours IS NULL OR due_after_hours BETWEEN 1 AND 8760),
    frequency text NOT NULL CHECK (frequency IN ('daily','weekly','monthly')),
    interval_count integer NOT NULL CHECK (interval_count BETWEEN 1 AND 365),
    weekday integer CHECK (weekday BETWEEN 1 AND 7),
    day_of_month integer CHECK (day_of_month BETWEEN 1 AND 31),
    time_of_day text NOT NULL CHECK (time_of_day ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    timezone text NOT NULL CHECK (timezone <> ''),
    starts_on date NOT NULL,
    active boolean NOT NULL DEFAULT true,
    -- The next scheduled run; NULL exactly while the definition is paused.
    next_run_at timestamptz,
    last_generated_at timestamptz,
    created_by_user_id uuid,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT recurring_weekday_for_weekly CHECK ((frequency = 'weekly') = (weekday IS NOT NULL)),
    CONSTRAINT recurring_day_for_monthly CHECK ((frequency = 'monthly') = (day_of_month IS NOT NULL)),
    CONSTRAINT recurring_next_run_matches_active CHECK (active = (next_run_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS recurring_task_definitions_due_idx
    ON platform.recurring_task_definitions(next_run_at) WHERE active;

-- Generated tasks remember their definition and the run they stand for; the
-- unique pair makes generation idempotent. There is deliberately no foreign
-- key: deleting a definition keeps the tasks it generated.
ALTER TABLE platform.tasks
    ADD COLUMN IF NOT EXISTS recurrence_definition_id uuid,
    ADD COLUMN IF NOT EXISTS scheduled_for timestamptz;
ALTER TABLE platform.tasks
    ADD CONSTRAINT tasks_recurrence_pair CHECK ((recurrence_definition_id IS NULL) = (scheduled_for IS NULL));
CREATE UNIQUE INDEX IF NOT EXISTS tasks_recurrence_run_unique
    ON platform.tasks(recurrence_definition_id, scheduled_for) WHERE recurrence_definition_id IS NOT NULL;
