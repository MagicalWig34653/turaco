-- Simulation round 3 follow-up (servicedesk): the employee's patient-impact signal, the affected person's location at
-- the time of the report, device text for search, and the duplicate relation. Forward-only; every new column is
-- nullable or has a default, so a running older binary keeps working until it is replaced.

-- The reporter says that patient care is affected (report a problem). It raises the priority of an employee's
-- ticket to high at creation (never above); staff see the flag and may filter on it.
ALTER TABLE servicedesk.tickets ADD COLUMN IF NOT EXISTS patient_impact boolean NOT NULL DEFAULT false;
-- The impact the reporter chose in the report form (a signal for triage; only patient_care changes the priority).
ALTER TABLE servicedesk.tickets ADD COLUMN IF NOT EXISTS reported_impact text CHECK (reported_impact IN ('patient_care', 'blocked', 'impaired', 'request'));

-- The affected person's primary Location when the ticket was raised (a snapshot, like device_snapshot: it does not
-- follow later moves of the person). Plain uuid without a foreign key into the organization schema.
ALTER TABLE servicedesk.tickets ADD COLUMN IF NOT EXISTS affected_location_id uuid;
CREATE INDEX IF NOT EXISTS tickets_affected_location_idx ON servicedesk.tickets (affected_location_id) WHERE affected_location_id IS NOT NULL;
-- One-time backfill of the snapshot from the current profile of the affected person.
UPDATE servicedesk.tickets t SET affected_location_id = u.primary_location_id
  FROM organization.users u
 WHERE u.id = t.affected_user_id AND u.primary_location_id IS NOT NULL AND t.affected_location_id IS NULL;

-- Searchable device text: reference, product, serial number and asset tag from the device snapshot. A generated column
-- keeps it in step with the snapshot without application code. ADD COLUMN ... STORED rewrites the table once.
ALTER TABLE servicedesk.tickets ADD COLUMN IF NOT EXISTS device_search text GENERATED ALWAYS AS (
    lower(coalesce(device_snapshot->>'reference', '') || ' ' || coalesce(device_snapshot->>'product', '') || ' ' ||
          coalesce(device_snapshot->>'serialNumber', '') || ' ' || coalesce(device_snapshot->>'assetTag', ''))) STORED NOT NULL;
CREATE INDEX IF NOT EXISTS tickets_device_search_trgm_idx ON servicedesk.tickets USING gin (device_search gin_trgm_ops);

-- A ticket marked as a duplicate points to the ticket that carries the work. The duplicate is cancelled with the
-- status reason "duplicate"; a ticket is never a duplicate of itself, and the pair cannot form a chain (checked by the
-- operation inside the transaction).
ALTER TABLE servicedesk.tickets ADD COLUMN IF NOT EXISTS duplicate_of_ticket_id uuid REFERENCES servicedesk.tickets (id);
ALTER TABLE servicedesk.tickets DROP CONSTRAINT IF EXISTS tickets_duplicate_valid;
ALTER TABLE servicedesk.tickets ADD CONSTRAINT tickets_duplicate_valid
    CHECK (duplicate_of_ticket_id IS NULL OR (duplicate_of_ticket_id <> id AND status = 'cancelled'));
CREATE INDEX IF NOT EXISTS tickets_duplicate_of_idx ON servicedesk.tickets (duplicate_of_ticket_id) WHERE duplicate_of_ticket_id IS NOT NULL;
