-- Service Desk invariants that were only enforced in Go (F5 review).
ALTER TABLE servicedesk.tickets ADD CONSTRAINT tickets_resolved_matches CHECK (status <> 'resolved' OR resolved_at IS NOT NULL);
ALTER TABLE servicedesk.tickets ADD CONSTRAINT tickets_closed_matches CHECK ((status = 'closed') = (closed_at IS NOT NULL));
ALTER TABLE servicedesk.tickets ADD CONSTRAINT tickets_cancel_has_reason CHECK (status <> 'cancelled' OR status_reason IS NOT NULL);
ALTER TABLE servicedesk.major_incident_updates ADD CONSTRAINT major_incident_updates_status_known
    CHECK (status IN ('identified', 'investigating', 'mitigating', 'monitoring', 'resolved', 'closed'));
ALTER TABLE servicedesk.major_incidents ADD CONSTRAINT major_incidents_closed_matches CHECK ((status = 'closed') = (closed_at IS NOT NULL));
ALTER TABLE servicedesk.major_incidents ADD CONSTRAINT major_incidents_resolved_matches
    CHECK ((status IN ('resolved', 'closed')) = (resolved_at IS NOT NULL));

CREATE INDEX IF NOT EXISTS tickets_open_queue_idx ON servicedesk.tickets (queue_team_id, id DESC) WHERE queue_team_id IS NOT NULL AND status IN ('new', 'open', 'in_progress', 'waiting');
CREATE INDEX IF NOT EXISTS tickets_open_assignee_idx ON servicedesk.tickets (assignee_user_id, id DESC) WHERE assignee_user_id IS NOT NULL AND status IN ('new', 'open', 'in_progress', 'waiting');
