-- Editable runtime administration settings. Definitions (type, default, bounds) live in code
-- (internal/platform/settings); this table stores only values an administrator changed.
CREATE TABLE IF NOT EXISTS platform.settings (
    key text PRIMARY KEY CHECK (key ~ '^[a-z][a-z0-9_]{0,63}(\.[a-z][a-z0-9_]{0,63}){0,3}$'),
    value jsonb NOT NULL,
    version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
    updated_by uuid NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
