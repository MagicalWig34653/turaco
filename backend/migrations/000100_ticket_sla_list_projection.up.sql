-- F17 S-E: list, report and query-engine fields of the SLA projection. Like the columns of 000099 these are owned by
-- the clock projection (recomputed in the transaction that changes a clock) and never written elsewhere.
ALTER TABLE servicedesk.tickets ADD COLUMN sla_paused boolean NOT NULL DEFAULT false;
ALTER TABLE servicedesk.tickets ADD COLUMN sla_response_outcome text CHECK (sla_response_outcome IN ('met','breached','not_measured'));
ALTER TABLE servicedesk.tickets ADD COLUMN sla_resolution_outcome text CHECK (sla_resolution_outcome IN ('met','breached','not_measured'));
UPDATE servicedesk.tickets t SET
    sla_paused = EXISTS (SELECT 1 FROM servicedesk.ticket_sla_clocks c WHERE c.ticket_id = t.id AND c.status = 'paused'),
    sla_response_outcome = (SELECT CASE WHEN c.status = 'completed' AND c.breached_at IS NOT NULL THEN 'breached' WHEN c.status = 'completed' THEN 'met'
                                        WHEN c.status = 'cancelled' THEN 'not_measured' END
                            FROM servicedesk.ticket_sla_clocks c WHERE c.ticket_id = t.id AND c.kind = 'first_response'),
    sla_resolution_outcome = (SELECT CASE WHEN c.status = 'completed' AND c.breached_at IS NOT NULL THEN 'breached' WHEN c.status = 'completed' THEN 'met'
                                          WHEN c.status = 'cancelled' THEN 'not_measured' END
                              FROM servicedesk.ticket_sla_clocks c WHERE c.ticket_id = t.id AND c.kind = 'resolution')
WHERE t.sla_tracked;
-- The SLA report reads completed clocks by completion time and cancelled clocks by their transition time.
CREATE INDEX ticket_sla_report_idx ON servicedesk.ticket_sla_clocks(completed_at) WHERE status = 'completed';
CREATE INDEX ticket_sla_cancel_idx ON servicedesk.ticket_sla_clock_transitions(occurred_at) WHERE to_status = 'cancelled';
-- Sorting the Ticket list by due time across Queues (query engine sort indexes; Tickets without a running clock sort last).
CREATE INDEX tickets_sla_response_sort_idx ON servicedesk.tickets(sla_response_due_at, id);
CREATE INDEX tickets_sla_resolution_sort_idx ON servicedesk.tickets(sla_resolution_due_at, id);
