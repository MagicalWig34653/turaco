-- Hardening from the F2 slice 2 database review.

-- Task list: the keyset order and the assignee lookups were unindexed. The
-- expressions must match the list query in modules/tasks/repository.
CREATE INDEX IF NOT EXISTS tasks_list_order_idx ON platform.tasks (
    (coalesce(due_at, 'infinity'::timestamptz)),
    (CASE priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END),
    id
);
CREATE INDEX IF NOT EXISTS tasks_assigned_user_idx ON platform.tasks(assigned_user_id) WHERE assigned_user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS tasks_assigned_team_idx ON platform.tasks(assigned_team_id) WHERE assigned_team_id IS NOT NULL;

-- Never used: the current-membership predicate of the queries cannot be
-- proven from this partial index.
DROP INDEX IF EXISTS organization.team_memberships_user_current_idx;

-- A membership must not end before it starts.
ALTER TABLE organization.team_memberships
    ADD CONSTRAINT team_memberships_interval_valid CHECK (valid_until IS NULL OR valid_until >= valid_from);

ALTER TABLE platform.tasks
    ADD CONSTRAINT tasks_completed_by_matches_status CHECK (status = 'completed' OR completed_by_user_id IS NULL),
    DROP CONSTRAINT tasks_title_not_empty,
    ADD CONSTRAINT tasks_title_not_blank CHECK (btrim(title) <> '');
