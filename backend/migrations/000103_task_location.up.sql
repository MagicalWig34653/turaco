-- Location of a Task for Location-scoped access (ADR-0040, F16 slice L-G). NULL means no Location: such Tasks are
-- visible to global holders (and to assignees) only. No foreign key into Organization: the module owning Locations is
-- reached through its public contract.
ALTER TABLE platform.tasks ADD COLUMN location_id uuid;
CREATE INDEX tasks_location_idx ON platform.tasks (location_id, id DESC) WHERE location_id IS NOT NULL;
