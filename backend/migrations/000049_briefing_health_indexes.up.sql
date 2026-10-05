-- Bounded F8c health and backlog reads. The migrator wraps this file in a transaction.
CREATE INDEX IF NOT EXISTS external_references_health_idx
    ON platform.external_references (system, entity_type, sync_state, updated_at)
    WHERE sync_state <> 'synced';

CREATE INDEX IF NOT EXISTS tickets_unassigned_open_idx
    ON servicedesk.tickets (id)
    WHERE assignee_user_id IS NULL AND status IN ('new', 'open', 'in_progress', 'waiting');
