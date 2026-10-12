CREATE TABLE platform.business_calendars (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    key text NOT NULL UNIQUE CHECK (key ~ '^[a-z][a-z0-9_-]{0,63}$'),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    timezone text NOT NULL,
    weekly_hours jsonb NOT NULL,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE platform.business_calendar_closures (
    calendar_id uuid NOT NULL REFERENCES platform.business_calendars(id) ON DELETE CASCADE,
    closed_on date NOT NULL,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    PRIMARY KEY (calendar_id, closed_on)
);
