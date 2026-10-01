-- F1 slice 5/6: roles, role assignments and audit query indexes.
-- See docs/security/identity-access-design.md §5 and §9.
-- Platform tables carry no foreign keys into business schemas.

CREATE TABLE IF NOT EXISTS platform.roles (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    key text NOT NULL CHECK (key ~ '^[a-z0-9][a-z0-9-]{1,62}$'),
    name text NOT NULL CHECK (name <> '' AND length(name) <= 200),
    description text NOT NULL DEFAULT '' CHECK (length(description) <= 2000),
    built_in boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    -- Roles are soft-deleted so assignment history keeps its role.
    deleted_at timestamptz,
    CHECK (NOT (built_in AND deleted_at IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS roles_key_active_unique ON platform.roles(key) WHERE deleted_at IS NULL;

-- The built-in administrator role implicitly holds every registered
-- permission; it never has role_permissions rows and cannot be changed.
INSERT INTO platform.roles (key, name, description, built_in)
VALUES ('platform-administrator', 'Platform administrator', 'All permissions. Built-in and immutable.', true)
ON CONFLICT (key) WHERE deleted_at IS NULL DO NOTHING;

CREATE TABLE IF NOT EXISTS platform.role_permissions (
    role_id uuid NOT NULL REFERENCES platform.roles(id) ON DELETE CASCADE,
    permission text NOT NULL CHECK (permission <> ''),
    PRIMARY KEY (role_id, permission)
);

CREATE TABLE IF NOT EXISTS platform.role_assignments (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    role_id uuid NOT NULL REFERENCES platform.roles(id),
    subject_type text NOT NULL CHECK (subject_type IN ('user', 'directory_group')),
    -- organization.users.id or organization.directory_groups.id (no FK: platform
    -- must not depend on business schemas; existence is checked on assignment).
    subject_id uuid NOT NULL,
    scope text NOT NULL DEFAULT 'global' CHECK (scope = 'global'),
    created_at timestamptz NOT NULL DEFAULT now(),
    -- {"userId": ...} for a session user or {"actor": "cli", "osUser": ...}.
    created_by jsonb NOT NULL,
    revoked_at timestamptz,
    revoked_by jsonb,
    CHECK ((revoked_at IS NULL) = (revoked_by IS NULL)),
    CHECK (revoked_at IS NULL OR revoked_at >= created_at)
);
CREATE UNIQUE INDEX IF NOT EXISTS role_assignments_active_unique
    ON platform.role_assignments(role_id, subject_type, subject_id, scope) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS role_assignments_subject_active_idx
    ON platform.role_assignments(subject_type, subject_id) WHERE revoked_at IS NULL;

-- Audit query (GET /api/v1/audit-events): newest first, keyset-paged by
-- (occurred_at DESC, id DESC). Every index ends in the sort key so each filter
-- shape reads rows already ordered; (target_type, target_id, occurred_at DESC)
-- from 000002 and correlation_id serve the target and correlation filters.
-- action uses text_pattern_ops for the range-predicate prefix filter.
CREATE INDEX IF NOT EXISTS audit_events_occurred_idx ON platform.audit_events(occurred_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS audit_events_actor_idx ON platform.audit_events(actor_id, occurred_at DESC, id DESC) WHERE actor_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS audit_events_action_idx ON platform.audit_events(action text_pattern_ops, occurred_at DESC, id DESC);
