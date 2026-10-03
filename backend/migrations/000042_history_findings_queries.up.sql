-- Endpoints (F6 slice 4): history, diff, Turaco-derived findings and device list queries.

-- A Turaco-derived finding that an assignment is configured and expected to apply but the provider shows
-- nothing (or not_applicable) for it. It is distinct from provider_reported_error, which is the provider's
-- own statement. Added NOT VALID and validated separately, so the table is not held under a long lock.
ALTER TABLE endpoints.findings DROP CONSTRAINT IF EXISTS findings_kind_check;
ALTER TABLE endpoints.findings ADD CONSTRAINT findings_kind_check
    CHECK (kind IN ('no_asset_match', 'serial_conflict', 'duplicate_device', 'unmatched_software', 'provider_reported_error', 'assignment_ineffective')) NOT VALID;
ALTER TABLE endpoints.findings VALIDATE CONSTRAINT findings_kind_check;

-- Device management history reads the observation history by device (the pair index leads with the artifact).
CREATE INDEX IF NOT EXISTS management_observation_history_device_idx
    ON endpoints.management_observation_history (device_id, observed_at, id);

-- Assignment history of the artifacts that address a Device: closed rows included (the existing reverse-lookup
-- indexes are partial on current rows).
CREATE INDEX IF NOT EXISTS management_assignments_history_group_idx
    ON endpoints.management_assignments (provider, target_group_external_id, artifact_id) WHERE target_group_external_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS management_assignments_history_kind_idx
    ON endpoints.management_assignments (provider, target_kind, artifact_id) WHERE target_kind <> 'group';

-- Device list filters: observed state of any artifact, OS version prefix, last check-in.
CREATE INDEX IF NOT EXISTS management_observations_problem_idx
    ON endpoints.management_observations (normalized_state, device_id)
    WHERE retired_at IS NULL AND normalized_state IN ('failed', 'conflict', 'pending');
CREATE INDEX IF NOT EXISTS devices_os_version_idx
    ON endpoints.devices (os_version text_pattern_ops) WHERE deleted_observed_at IS NULL AND os_version IS NOT NULL;
CREATE INDEX IF NOT EXISTS devices_last_checkin_idx
    ON endpoints.devices (last_checkin_at) WHERE deleted_observed_at IS NULL AND last_checkin_at IS NOT NULL;
