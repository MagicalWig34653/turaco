-- Ticket Queues (ADR-0033, F13 slice Q-C): a Queue is a Service Desk desk (IT, HR, Facility) with its own key,
-- prefix and committed number counter, explicit grants per subject, and a reference registry that keeps every
-- issued display number (current or alias) globally unique and never reused.
--
-- Existing Tickets move into the default Queue 'it' with the legacy prefix TKT: their references, numbers and
-- every link, email or external reference to them stay valid. Forward-only; the old global sequence
-- servicedesk.ticket_number_seq stays in place (unused) and is not dropped in this release.

CREATE TABLE IF NOT EXISTS servicedesk.queues (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    -- Stable contract value for other modules; frozen.
    key text NOT NULL CHECK (key ~ '^[a-z][a-z0-9-]{1,30}$'),
    -- Part of every issued number; frozen (and unique among all current and former Queues).
    prefix text NOT NULL CHECK (prefix ~ '^[A-Z][A-Z0-9]{1,7}$'),
    name text NOT NULL CHECK (name = btrim(name) AND length(name) BETWEEN 1 AND 80),
    description text CHECK (description IS NULL OR length(description) <= 500),
    -- Neutral desk label shown to a requester who may not see the Queue itself (default: the name).
    public_label text CHECK (public_label IS NULL OR (public_label = btrim(public_label) AND length(public_label) BETWEEN 1 AND 80)),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    -- public: every signed-in User may raise Tickets into it (when routing allows employee choice).
    visibility text NOT NULL DEFAULT 'internal' CHECK (visibility IN ('internal', 'public')),
    routing_mode text NOT NULL DEFAULT 'employee_choice' CHECK (routing_mode IN ('employee_choice', 'automatic', 'both')),
    default_priority text NOT NULL DEFAULT 'normal' CHECK (default_priority IN ('low', 'normal', 'high', 'urgent')),
    -- Assignment hint written to tickets.queue_team_id (routing Team); not authorization.
    default_team_id uuid,
    default_for_intake boolean NOT NULL DEFAULT false,
    -- The committed counter: the next number to issue. Locked by the issuing transaction; never exposed by the API.
    next_number bigint NOT NULL DEFAULT 1 CHECK (next_number >= 1),
    number_padding smallint NOT NULL DEFAULT 4 CHECK (number_padding BETWEEN 1 AND 9),
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    archived_at timestamptz,
    CONSTRAINT queues_key_unique UNIQUE (key),
    CONSTRAINT queues_prefix_unique UNIQUE (prefix),
    CONSTRAINT queues_archived_matches CHECK ((status = 'archived') = (archived_at IS NOT NULL)),
    CONSTRAINT queues_default_active CHECK (NOT default_for_intake OR status = 'active')
);
CREATE UNIQUE INDEX IF NOT EXISTS queues_one_default_idx ON servicedesk.queues (default_for_intake) WHERE default_for_intake;

CREATE OR REPLACE FUNCTION servicedesk.queues_freeze() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.key IS DISTINCT FROM OLD.key OR NEW.prefix IS DISTINCT FROM OLD.prefix OR NEW.number_padding IS DISTINCT FROM OLD.number_padding
       OR NEW.id IS DISTINCT FROM OLD.id OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'queue key, prefix and number padding are frozen' USING ERRCODE = 'SD409';
    END IF;
    IF NEW.next_number < OLD.next_number THEN
        RAISE EXCEPTION 'the number counter of a queue never moves backwards' USING ERRCODE = 'SD409';
    END IF;
    RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS queues_freeze ON servicedesk.queues;
CREATE TRIGGER queues_freeze BEFORE UPDATE ON servicedesk.queues FOR EACH ROW EXECUTE FUNCTION servicedesk.queues_freeze();

-- Explicit grants. Levels view, work and manage are cumulative; create is independent (raise Tickets into a Queue
-- without seeing it); every other level also allows creating.
CREATE TABLE IF NOT EXISTS servicedesk.queue_grants (
    queue_id uuid NOT NULL REFERENCES servicedesk.queues(id) ON DELETE CASCADE,
    subject_type text NOT NULL CHECK (subject_type IN ('user', 'team', 'role')),
    subject_id uuid NOT NULL,
    level text NOT NULL CHECK (level IN ('create', 'view', 'work', 'manage')),
    granted_by uuid,
    granted_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (queue_id, subject_type, subject_id, level)
);
CREATE INDEX IF NOT EXISTS queue_grants_subject_idx ON servicedesk.queue_grants (subject_type, subject_id);

-- The default Queue. TKT is kept as its legacy prefix with the legacy 6 digit width, so no issued reference changes.
DO $$
DECLARE
    v_queue uuid;
    v_next bigint;
    v_seq_last bigint;
    v_seq_called boolean;
BEGIN
    IF EXISTS (SELECT 1 FROM servicedesk.tickets WHERE reference !~ '^TKT-[0-9]+$') THEN
        RAISE EXCEPTION 'tickets with a reference outside the legacy TKT-<number> format exist';
    END IF;
    SELECT last_value, is_called INTO v_seq_last, v_seq_called FROM servicedesk.ticket_number_seq;
    SELECT GREATEST(COALESCE(max(substring(reference FROM 5)::bigint), 0) + 1,
                    CASE WHEN v_seq_called THEN v_seq_last + 1 ELSE v_seq_last END, 1)
      INTO v_next FROM servicedesk.tickets;
    INSERT INTO servicedesk.queues (key, prefix, name, status, visibility, routing_mode, default_for_intake, next_number, number_padding)
    VALUES ('it', 'TKT', 'IT', 'active', 'public', 'both', true, v_next, 6)
    ON CONFLICT (key) DO NOTHING;
    SELECT id INTO v_queue FROM servicedesk.queues WHERE key = 'it';

    ALTER TABLE servicedesk.tickets ADD COLUMN IF NOT EXISTS queue_id uuid REFERENCES servicedesk.queues(id);
    ALTER TABLE servicedesk.tickets ADD COLUMN IF NOT EXISTS number bigint;
    UPDATE servicedesk.tickets SET queue_id = v_queue, number = substring(reference FROM 5)::bigint WHERE queue_id IS NULL;
END $$;

ALTER TABLE servicedesk.tickets ALTER COLUMN queue_id SET NOT NULL;
ALTER TABLE servicedesk.tickets ALTER COLUMN number SET NOT NULL;
ALTER TABLE servicedesk.tickets ADD CONSTRAINT tickets_number_positive CHECK (number >= 1);
ALTER TABLE servicedesk.tickets ADD CONSTRAINT tickets_queue_number_unique UNIQUE (queue_id, number);
-- The reference is issued by the trigger below, never by the old global sequence.
ALTER TABLE servicedesk.tickets ALTER COLUMN reference DROP DEFAULT;

CREATE INDEX IF NOT EXISTS tickets_queue_id_idx ON servicedesk.tickets (queue_id, id DESC);
CREATE INDEX IF NOT EXISTS tickets_queue_open_idx ON servicedesk.tickets (queue_id, id) WHERE status IN ('new', 'open', 'in_progress', 'waiting');
CREATE INDEX IF NOT EXISTS tickets_queue_unassigned_idx ON servicedesk.tickets (queue_id, id)
    WHERE assignee_user_id IS NULL AND status IN ('new', 'open', 'in_progress', 'waiting');

-- Every issued display number, current or alias. One primary key makes a reference globally unique; rows are never
-- deleted by the application and never reassigned to another Ticket.
CREATE TABLE IF NOT EXISTS servicedesk.reference_registry (
    reference text PRIMARY KEY CHECK (reference ~ '^[A-Z][A-Z0-9]{1,7}-[0-9]+$'),
    ticket_id uuid NOT NULL REFERENCES servicedesk.tickets(id) ON DELETE CASCADE,
    -- The Queue the number was issued from.
    queue_id uuid NOT NULL REFERENCES servicedesk.queues(id),
    kind text NOT NULL CHECK (kind IN ('current', 'alias')),
    issued_at timestamptz NOT NULL DEFAULT now(),
    retired_at timestamptz,
    CONSTRAINT reference_registry_retired_matches CHECK ((kind = 'alias') = (retired_at IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS reference_registry_current_idx ON servicedesk.reference_registry (ticket_id) WHERE kind = 'current';
CREATE INDEX IF NOT EXISTS reference_registry_ticket_idx ON servicedesk.reference_registry (ticket_id, issued_at);

INSERT INTO servicedesk.reference_registry (reference, ticket_id, queue_id, kind, issued_at)
SELECT reference, id, queue_id, 'current', created_at FROM servicedesk.tickets
ON CONFLICT (reference) DO NOTHING;

-- Issues the next number of a Queue. The UPDATE takes the Queue row lock, so concurrent issues in one Queue
-- serialize and different Queues do not contend. The counter rolls back with the surrounding transaction, so only
-- committed numbers are permanent; a number that never committed can be issued again, which nobody could have seen.
-- SD404: the Queue does not exist or is archived.
CREATE OR REPLACE FUNCTION servicedesk.issue_reference(p_queue uuid, OUT o_number bigint, OUT o_reference text)
LANGUAGE plpgsql AS $$
DECLARE
    v_prefix text;
    v_padding smallint;
BEGIN
    UPDATE servicedesk.queues SET next_number = next_number + 1
     WHERE id = p_queue AND status = 'active'
    RETURNING prefix, number_padding, next_number - 1 INTO v_prefix, v_padding, o_number;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'queue is unknown or archived' USING ERRCODE = 'SD404';
    END IF;
    o_reference := v_prefix || '-' || CASE WHEN length(o_number::text) >= v_padding THEN o_number::text ELSE lpad(o_number::text, v_padding, '0') END;
END $$;

-- New Tickets: the Queue defaults to the intake Queue; number and reference come from the Queue counter in the same
-- statement and transaction as the insert. Whatever a caller writes into number or reference is replaced.
CREATE OR REPLACE FUNCTION servicedesk.tickets_issue_reference() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    r record;
BEGIN
    IF NEW.queue_id IS NULL THEN
        SELECT id INTO NEW.queue_id FROM servicedesk.queues WHERE default_for_intake AND status = 'active';
        IF NEW.queue_id IS NULL THEN
            RAISE EXCEPTION 'no active intake queue' USING ERRCODE = 'SD404';
        END IF;
    END IF;
    SELECT * INTO r FROM servicedesk.issue_reference(NEW.queue_id);
    NEW.number := r.o_number;
    NEW.reference := r.o_reference;
    RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS tickets_issue_reference ON servicedesk.tickets;
CREATE TRIGGER tickets_issue_reference BEFORE INSERT ON servicedesk.tickets FOR EACH ROW EXECUTE FUNCTION servicedesk.tickets_issue_reference();

CREATE OR REPLACE FUNCTION servicedesk.tickets_register_reference() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO servicedesk.reference_registry (reference, ticket_id, queue_id, kind) VALUES (NEW.reference, NEW.id, NEW.queue_id, 'current');
    RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS tickets_register_reference ON servicedesk.tickets;
CREATE TRIGGER tickets_register_reference AFTER INSERT ON servicedesk.tickets FOR EACH ROW EXECUTE FUNCTION servicedesk.tickets_register_reference();

-- A Ticket changes Queue, number and reference together or not at all (the move operation); the previous reference
-- becomes an alias that is never deleted or reassigned.
CREATE OR REPLACE FUNCTION servicedesk.tickets_guard_move() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.queue_id IS DISTINCT FROM OLD.queue_id THEN
        IF NEW.reference IS NOT DISTINCT FROM OLD.reference THEN
            RAISE EXCEPTION 'moving a ticket to another queue issues a new number' USING ERRCODE = 'SD409';
        END IF;
    ELSIF NEW.reference IS DISTINCT FROM OLD.reference OR NEW.number IS DISTINCT FROM OLD.number THEN
        RAISE EXCEPTION 'the reference of a ticket only changes when it moves to another queue' USING ERRCODE = 'SD409';
    END IF;
    RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS tickets_guard_move ON servicedesk.tickets;
CREATE TRIGGER tickets_guard_move BEFORE UPDATE OF queue_id, number, reference ON servicedesk.tickets FOR EACH ROW EXECUTE FUNCTION servicedesk.tickets_guard_move();

CREATE OR REPLACE FUNCTION servicedesk.tickets_retire_reference() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.reference IS DISTINCT FROM OLD.reference THEN
        UPDATE servicedesk.reference_registry SET kind = 'alias', retired_at = now() WHERE reference = OLD.reference AND ticket_id = OLD.id;
        INSERT INTO servicedesk.reference_registry (reference, ticket_id, queue_id, kind) VALUES (NEW.reference, NEW.id, NEW.queue_id, 'current');
    END IF;
    RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS tickets_retire_reference ON servicedesk.tickets;
CREATE TRIGGER tickets_retire_reference AFTER UPDATE OF reference ON servicedesk.tickets FOR EACH ROW EXECUTE FUNCTION servicedesk.tickets_retire_reference();
