-- Team write operations (F2 slice 2): platform-owned Teams get unique active
-- names and at most one current membership per (team, user).
CREATE UNIQUE INDEX IF NOT EXISTS teams_active_name_unique
    ON organization.teams(lower(name)) WHERE active;

CREATE UNIQUE INDEX IF NOT EXISTS team_memberships_current_unique
    ON organization.team_memberships(team_id, user_id) WHERE valid_until IS NULL;

-- The current-members read and the task module's "my teams" lookup go by user.
CREATE INDEX IF NOT EXISTS team_memberships_user_current_idx
    ON organization.team_memberships(user_id, team_id) WHERE valid_until IS NULL;
