-- Platform query engine (ADR-0033, F13 slice Q-A): indexes that serve the sort keys of the Ticket, Task and
-- Device catalogs. A catalog may only declare a sort as indexed when an index below (or an older one) serves it;
-- the query engine refuses unindexed sorts (query.unindexed_sort). The CASE expressions must match the
-- Ordinal sort expressions the catalogs build (urgent, high, normal, low; anything else last).
-- Migration numbers 000060 to 000062 stay reserved for the later F13 slices (Views, Queues, Boards).

CREATE INDEX IF NOT EXISTS tickets_created_idx ON servicedesk.tickets (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS tickets_updated_idx ON servicedesk.tickets (updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS tickets_priority_rank_idx ON servicedesk.tickets (
    (CASE priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 WHEN 'low' THEN 3 ELSE 4 END), id
);

CREATE INDEX IF NOT EXISTS tasks_query_due_idx ON platform.tasks (
    due_at, (CASE priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 WHEN 'low' THEN 3 ELSE 4 END), id
);
CREATE INDEX IF NOT EXISTS tasks_query_priority_rank_idx ON platform.tasks (
    (CASE priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 WHEN 'low' THEN 3 ELSE 4 END), id
);
CREATE INDEX IF NOT EXISTS tasks_query_created_idx ON platform.tasks (created_at, id);
CREATE INDEX IF NOT EXISTS tasks_query_updated_idx ON platform.tasks (updated_at, id);

CREATE INDEX IF NOT EXISTS devices_query_name_idx ON endpoints.devices (lower(name), id);
CREATE INDEX IF NOT EXISTS devices_query_checkin_idx ON endpoints.devices (last_checkin_at, id);
