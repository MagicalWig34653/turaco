-- Hardening of the Ticket move guard (F13 slice Q-C security review). Forward-only; replaces functions of 000061.
--
-- A Ticket changes Queue only together with a new reference, and that reference must be a real one: it must
-- differ from the old one, carry the prefix of the target Queue with a matching number (the numeric part may
-- repeat: IT-7 can become HR-7), and have been issued by servicedesk.issue_reference in the same transaction.
-- issue_reference records each reference it issues in the transaction-local setting turaco.issued_references,
-- which the guard checks. This protects the invariant against application bugs and ad-hoc SQL; it is not a defence
-- against a session that sets the setting on purpose (such a session can write the table anyway).

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
    PERFORM set_config('turaco.issued_references',
        CASE WHEN coalesce(current_setting('turaco.issued_references', true), '') = '' THEN o_reference
             ELSE current_setting('turaco.issued_references', true) || ',' || o_reference END, true);
END $$;

CREATE OR REPLACE FUNCTION servicedesk.tickets_guard_move() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    v_prefix text;
BEGIN
    IF NEW.queue_id IS DISTINCT FROM OLD.queue_id THEN
        IF NEW.reference IS NOT DISTINCT FROM OLD.reference THEN
            RAISE EXCEPTION 'moving a ticket to another queue issues a new number' USING ERRCODE = 'SD409';
        END IF;
        SELECT prefix INTO v_prefix FROM servicedesk.queues WHERE id = NEW.queue_id;
        IF v_prefix IS NULL OR NEW.reference !~ ('^' || v_prefix || '-[0-9]{1,18}$')
           OR substring(NEW.reference FROM length(v_prefix) + 2)::bigint IS DISTINCT FROM NEW.number THEN
            RAISE EXCEPTION 'the new number does not belong to the target queue' USING ERRCODE = 'SD409';
        END IF;
        IF NOT (NEW.reference = ANY (string_to_array(coalesce(current_setting('turaco.issued_references', true), ''), ','))) THEN
            RAISE EXCEPTION 'the new number was not issued in this transaction' USING ERRCODE = 'SD409';
        END IF;
    ELSIF NEW.reference IS DISTINCT FROM OLD.reference OR NEW.number IS DISTINCT FROM OLD.number THEN
        RAISE EXCEPTION 'the reference of a ticket only changes when it moves to another queue' USING ERRCODE = 'SD409';
    END IF;
    RETURN NEW;
END $$;
