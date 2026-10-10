-- Major Incident exercise flag, owner, next update due and affected Locations; briefing item audience.
-- Locations and owners are Organization ids held without cross-schema foreign keys (validated through the
-- Organization public contract when set).
ALTER TABLE servicedesk.major_incidents
    ADD COLUMN IF NOT EXISTS is_exercise boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS owner_user_id uuid,
    ADD COLUMN IF NOT EXISTS next_update_due timestamptz;

CREATE TABLE IF NOT EXISTS servicedesk.major_incident_locations (
    major_incident_id uuid NOT NULL REFERENCES servicedesk.major_incidents(id) ON DELETE CASCADE,
    location_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (major_incident_id, location_id)
);

-- 'it' items stay inside the IT briefing; 'all' items are also announced to every signed-in User.
ALTER TABLE briefing.items
    ADD COLUMN IF NOT EXISTS audience text NOT NULL DEFAULT 'it' CHECK (audience IN ('it', 'all'));
