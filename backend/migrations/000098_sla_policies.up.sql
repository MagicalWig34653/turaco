CREATE TABLE servicedesk.sla_policies (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    key text NOT NULL UNIQUE CHECK (key ~ '^[a-z][a-z0-9_-]{0,63}$'),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    description text NOT NULL DEFAULT '' CHECK (length(description) <= 2000),
    calendar_id uuid REFERENCES platform.business_calendars(id),
    pause_waiting_reasons text[] NOT NULL DEFAULT ARRAY['customer']::text[],
    warning_percent smallint NOT NULL DEFAULT 80 CHECK (warning_percent BETWEEN 50 AND 95),
    escalation_task boolean NOT NULL DEFAULT false,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (pause_waiting_reasons <@ ARRAY['customer','vendor','external_service','scheduled_change','hardware']::text[])
);
CREATE INDEX sla_policies_active_calendar_idx ON servicedesk.sla_policies (calendar_id) WHERE status = 'active';
CREATE TABLE servicedesk.sla_targets (
    policy_id uuid NOT NULL REFERENCES servicedesk.sla_policies(id) ON DELETE CASCADE,
    priority text NOT NULL CHECK (priority IN ('low','normal','high','urgent')),
    kind text NOT NULL CHECK (kind IN ('first_response','resolution')),
    target_minutes integer NOT NULL CHECK (target_minutes BETWEEN 1 AND 129600),
    time_basis text NOT NULL CHECK (time_basis IN ('business','continuous')),
    PRIMARY KEY (policy_id, priority, kind)
);
ALTER TABLE servicedesk.queues ADD COLUMN sla_policy_id uuid REFERENCES servicedesk.sla_policies(id);
CREATE INDEX queues_sla_policy_idx ON servicedesk.queues (sla_policy_id) WHERE sla_policy_id IS NOT NULL;
