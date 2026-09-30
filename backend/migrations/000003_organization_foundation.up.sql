CREATE TABLE IF NOT EXISTS organization.locations (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    name text NOT NULL,
    external_key text,
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS organization.departments (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    name text NOT NULL,
    parent_department_id uuid REFERENCES organization.departments(id),
    external_key text,
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS organization.users (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    display_name text NOT NULL,
    given_name text,
    family_name text,
    primary_email text,
    employee_number text,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','inactive','departed','external','unknown')),
    department_id uuid REFERENCES organization.departments(id),
    primary_location_id uuid REFERENCES organization.locations(id),
    manager_user_id uuid REFERENCES organization.users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS users_primary_email_unique ON organization.users(lower(primary_email)) WHERE primary_email IS NOT NULL;

CREATE TABLE IF NOT EXISTS organization.external_identities (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    user_id uuid NOT NULL REFERENCES organization.users(id),
    provider_key text NOT NULL,
    external_subject text NOT NULL,
    username text,
    distinguished_name text,
    enabled boolean NOT NULL DEFAULT true,
    last_seen_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(provider_key, external_subject)
);

CREATE TABLE IF NOT EXISTS organization.teams (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    name text NOT NULL,
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS organization.team_memberships (
    team_id uuid NOT NULL REFERENCES organization.teams(id),
    user_id uuid NOT NULL REFERENCES organization.users(id),
    role text,
    valid_from timestamptz NOT NULL DEFAULT now(),
    valid_until timestamptz,
    source text NOT NULL DEFAULT 'platform',
    PRIMARY KEY(team_id, user_id, valid_from)
);
