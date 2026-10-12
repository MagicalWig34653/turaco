CREATE TABLE servicedesk.ticket_sla_clocks (
    ticket_id uuid NOT NULL REFERENCES servicedesk.tickets(id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('first_response','resolution')),
    status text NOT NULL CHECK (status IN ('running','paused','completed','cancelled')),
    policy_id uuid NOT NULL REFERENCES servicedesk.sla_policies(id),
    policy_version integer NOT NULL CHECK (policy_version > 0),
    target_minutes integer NOT NULL CHECK (target_minutes BETWEEN 1 AND 129600),
    time_basis text NOT NULL CHECK (time_basis IN ('business','continuous')),
    calendar_id uuid REFERENCES platform.business_calendars(id),
    warning_percent smallint NOT NULL CHECK (warning_percent BETWEEN 50 AND 95),
    started_at timestamptz NOT NULL,
    elapsed_seconds bigint NOT NULL DEFAULT 0 CHECK (elapsed_seconds >= 0),
    running_since timestamptz,
    due_at timestamptz,
    warn_at timestamptz,
    paused_at timestamptz,
    pause_reason text,
    completed_at timestamptz,
    end_reason text,
    warned_at timestamptz,
    breached_at timestamptz,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    PRIMARY KEY (ticket_id, kind),
    CHECK ((time_basis = 'business' AND calendar_id IS NOT NULL) OR (time_basis = 'continuous')),
    CHECK (breached_at IS NULL OR breached_at >= started_at),
    CHECK ((status = 'running' AND running_since IS NOT NULL AND due_at IS NOT NULL AND warn_at IS NOT NULL AND paused_at IS NULL AND pause_reason IS NULL AND completed_at IS NULL AND end_reason IS NULL)
        OR (status = 'paused' AND running_since IS NULL AND due_at IS NULL AND warn_at IS NULL AND paused_at IS NOT NULL AND pause_reason IS NOT NULL AND completed_at IS NULL AND end_reason IS NULL)
        OR (status = 'completed' AND running_since IS NULL AND due_at IS NULL AND warn_at IS NULL AND paused_at IS NULL AND pause_reason IS NULL AND completed_at IS NOT NULL AND end_reason IS NULL)
        OR (status = 'cancelled' AND running_since IS NULL AND due_at IS NULL AND warn_at IS NULL AND paused_at IS NULL AND pause_reason IS NULL AND completed_at IS NULL AND end_reason IN ('ticket_cancelled','no_target','exempted')))
);
CREATE TABLE servicedesk.ticket_sla_clock_transitions (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    ticket_id uuid NOT NULL,
    kind text NOT NULL,
    operation text NOT NULL,
    from_status text,
    to_status text NOT NULL,
    cause text NOT NULL,
    actor_id uuid,
    occurred_at timestamptz NOT NULL,
    remaining_seconds bigint NOT NULL,
    FOREIGN KEY (ticket_id,kind) REFERENCES servicedesk.ticket_sla_clocks(ticket_id,kind) ON DELETE RESTRICT
);
CREATE FUNCTION servicedesk.prevent_sla_transition_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'SLA clock transitions are immutable';
END $$;
CREATE TRIGGER ticket_sla_transitions_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON servicedesk.ticket_sla_clock_transitions
    FOR EACH STATEMENT EXECUTE FUNCTION servicedesk.prevent_sla_transition_change();
ALTER TABLE servicedesk.tickets ADD COLUMN sla_response_due_at timestamptz;
ALTER TABLE servicedesk.tickets ADD COLUMN sla_resolution_due_at timestamptz;
ALTER TABLE servicedesk.tickets ADD COLUMN sla_warn_at timestamptz;
ALTER TABLE servicedesk.tickets ADD COLUMN sla_breached_at timestamptz;
ALTER TABLE servicedesk.tickets ADD COLUMN sla_tracked boolean NOT NULL DEFAULT false;
-- This migration runs in a transaction; CREATE INDEX CONCURRENTLY is unavailable. Upgrade takes brief index locks.
CREATE INDEX tickets_sla_resolution_due_idx ON servicedesk.tickets(queue_id,sla_resolution_due_at,id) WHERE status IN ('new','open','in_progress','waiting');
CREATE INDEX tickets_sla_response_due_idx ON servicedesk.tickets(queue_id,sla_response_due_at,id) WHERE status IN ('new','open','in_progress','waiting');
CREATE INDEX tickets_sla_breached_idx ON servicedesk.tickets(queue_id,sla_breached_at) WHERE status IN ('new','open','in_progress','waiting') AND sla_breached_at IS NOT NULL;
CREATE INDEX ticket_sla_due_idx ON servicedesk.ticket_sla_clocks(due_at) WHERE status='running' AND breached_at IS NULL;
CREATE INDEX ticket_sla_warn_idx ON servicedesk.ticket_sla_clocks(warn_at) WHERE status='running' AND warned_at IS NULL;
CREATE INDEX ticket_sla_calendar_idx ON servicedesk.ticket_sla_clocks(calendar_id) WHERE status IN ('running','paused');
