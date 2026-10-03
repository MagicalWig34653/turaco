-- Endpoints (F6 slice 4): history, diff, Turaco-derived findings and device list queries.

-- Index builds run inside the migration transaction and block writes to their table while they run; they are
-- bounded so a surprise on a large table fails the migration instead of holding the lock indefinitely. (The
-- endpoints tables are written by the sync only.)
SET LOCAL statement_timeout = '10min';

-- Device management history reads the observation history by device (the pair index leads with the artifact).
CREATE INDEX IF NOT EXISTS management_observation_history_device_idx
    ON endpoints.management_observation_history (device_id, observed_at, id);

-- Assignment history of the artifacts that address a Device: closed rows included (the existing reverse-lookup
-- indexes are partial on current rows). History queries find the previous and the following row of one provider
-- assignment by (artifact, provider assignment, valid_from, id).
CREATE INDEX IF NOT EXISTS management_assignments_history_group_idx
    ON endpoints.management_assignments (provider, target_group_external_id, artifact_id) WHERE target_group_external_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS management_assignments_history_kind_idx
    ON endpoints.management_assignments (provider, artifact_id) WHERE target_kind = 'all_devices';
CREATE INDEX IF NOT EXISTS management_assignments_chain_idx
    ON endpoints.management_assignments (artifact_id, provider_assignment_id, valid_from, id);

-- Device list filter on the observed state of any artifact. (No index on devices.os_version or last_checkin_at:
-- the device list is a keyset scan ordered by id, so such an index would need a sort of all matches and the planner
-- does not use it for the page query.)
CREATE INDEX IF NOT EXISTS management_observations_problem_idx
    ON endpoints.management_observations (normalized_state, device_id)
    WHERE retired_at IS NULL AND normalized_state IN ('failed', 'conflict', 'pending');

-- The two statements below and the constraint swap take ACCESS EXCLUSIVE locks until the migration commits. They
-- run last on tiny tables (one row per provider; one row per finding) and fail after 5s of waiting instead of
-- queueing behind a long transaction and blocking everything behind it.
SET LOCAL statement_timeout = '30s';
SET LOCAL lock_timeout = '5s';

-- The assignment_ineffective pass rotates through the live Devices over successive runs: the cursor is the id
-- after which the next pass starts. The row may exist before any sync completed.
ALTER TABLE endpoints.provider_sync_state ADD COLUMN IF NOT EXISTS ineffective_cursor uuid;
ALTER TABLE endpoints.provider_sync_state ALTER COLUMN last_completed_at DROP NOT NULL;

-- A Turaco-derived finding that an assignment is configured and expected to apply but the provider shows
-- nothing (or not_applicable) for it. It is distinct from provider_reported_error, which is the provider's
-- own statement. Last, so the lock on findings is held for the shortest time.
ALTER TABLE endpoints.findings DROP CONSTRAINT IF EXISTS findings_kind_check;
ALTER TABLE endpoints.findings ADD CONSTRAINT findings_kind_check
    CHECK (kind IN ('no_asset_match', 'serial_conflict', 'duplicate_device', 'unmatched_software', 'provider_reported_error', 'assignment_ineffective'));
