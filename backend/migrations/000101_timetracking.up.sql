-- F17 T-A: Time Tracking (module timetracking, docs/product/f17-sla-time-tracking-design.md section 7, ADR-0041).
-- Time entries are personal data about employees. The schema holds effort per Ticket (subject) and the person whose
-- time it is; nothing here is aggregated per person. Users, Tickets and Queues are referenced by id without foreign
-- keys across modules. Rows are removed only by the daily timetracking.purge job (retention).
CREATE SCHEMA IF NOT EXISTS timetracking;

-- Single settings row. The co-determination record is the precondition for enabling the module (ADR-0032 pattern).
CREATE TABLE timetracking.settings (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    codetermination_basis text CHECK (codetermination_basis IN ('agreement', 'not_applicable')),
    codetermination_recorded_on date,
    dpia_recorded_on date,
    edit_window_days smallint NOT NULL DEFAULT 14 CHECK (edit_window_days BETWEEN 1 AND 90),
    retention_months smallint NOT NULL DEFAULT 24 CHECK (retention_months BETWEEN 3 AND 120),
    show_entry_authors boolean NOT NULL DEFAULT true,
    min_report_group_size smallint NOT NULL DEFAULT 3 CHECK (min_report_group_size BETWEEN 2 AND 20),
    version integer NOT NULL DEFAULT 1,
    updated_by uuid,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT settings_basis_dated CHECK ((codetermination_basis IS NULL) = (codetermination_recorded_on IS NULL))
);
INSERT INTO timetracking.settings (singleton) VALUES (true) ON CONFLICT (singleton) DO NOTHING;

CREATE TABLE timetracking.time_entries (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    subject_type text NOT NULL CHECK (subject_type IN ('ticket')),
    subject_id uuid NOT NULL,
    -- Snapshot of the Ticket's Queue when the time was logged; used for report scoping, never updated afterwards
    -- except when the entry is moved to another Ticket.
    queue_id uuid NOT NULL,
    -- Whose time it is.
    user_id uuid NOT NULL,
    work_date date NOT NULL,
    duration_minutes integer NOT NULL CHECK (duration_minutes BETWEEN 1 AND 1440),
    started_at timestamptz,
    source text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'timer')),
    -- Internal note; rejected by the application when it contains unsafe characters. Never audited or sent anywhere.
    note text CHECK (note IS NULL OR length(note) BETWEEN 1 AND 500),
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    version integer NOT NULL DEFAULT 1,
    deleted_at timestamptz,
    deleted_by uuid,
    delete_reason text CHECK (delete_reason IN ('wrong_ticket', 'duplicate', 'wrong_duration', 'other')),
    CONSTRAINT time_entries_delete_together CHECK ((deleted_at IS NULL) = (deleted_by IS NULL) AND (deleted_at IS NULL) = (delete_reason IS NULL))
);
CREATE INDEX time_entries_subject_idx ON timetracking.time_entries (subject_type, subject_id) WHERE deleted_at IS NULL;
CREATE INDEX time_entries_user_date_idx ON timetracking.time_entries (user_id, work_date DESC) WHERE deleted_at IS NULL;
CREATE INDEX time_entries_queue_date_idx ON timetracking.time_entries (queue_id, work_date) WHERE deleted_at IS NULL;
CREATE INDEX time_entries_work_date_idx ON timetracking.time_entries (work_date);
CREATE INDEX time_entries_deleted_idx ON timetracking.time_entries (deleted_at) WHERE deleted_at IS NOT NULL;

-- Personal timer state of slice T-B (at most one running timer per person). Created now so the schema is complete.
CREATE TABLE timetracking.timers (
    user_id uuid PRIMARY KEY,
    subject_type text NOT NULL CHECK (subject_type IN ('ticket')),
    subject_id uuid NOT NULL,
    started_at timestamptz NOT NULL DEFAULT now(),
    note text CHECK (note IS NULL OR length(note) BETWEEN 1 AND 500)
);
