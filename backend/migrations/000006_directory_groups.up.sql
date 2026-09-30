CREATE TABLE IF NOT EXISTS organization.directory_groups (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    provider_key text NOT NULL,
    external_id text NOT NULL,
    display_name text NOT NULL,
    description text,
    first_observed_at timestamptz NOT NULL,
    last_observed_at timestamptz NOT NULL,
    deleted_observed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(provider_key, external_id),
    CHECK (last_observed_at >= first_observed_at)
);

CREATE TABLE IF NOT EXISTS organization.directory_group_memberships (
    group_id uuid NOT NULL REFERENCES organization.directory_groups(id),
    user_id uuid NOT NULL REFERENCES organization.users(id),
    last_observed_at timestamptz NOT NULL,
    PRIMARY KEY(group_id, user_id)
);
CREATE INDEX IF NOT EXISTS directory_group_memberships_user_idx ON organization.directory_group_memberships(user_id);

CREATE INDEX IF NOT EXISTS team_memberships_user_idx ON organization.team_memberships(user_id);

CREATE INDEX IF NOT EXISTS users_display_name_prefix_idx ON organization.users(lower(display_name) text_pattern_ops);
CREATE INDEX IF NOT EXISTS directory_groups_display_name_prefix_idx ON organization.directory_groups(lower(display_name) text_pattern_ops);
CREATE INDEX IF NOT EXISTS teams_name_prefix_idx ON organization.teams(lower(name) text_pattern_ops);
CREATE INDEX IF NOT EXISTS locations_name_prefix_idx ON organization.locations(lower(name) text_pattern_ops);
