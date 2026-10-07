-- Endpoints (F9 slice G4): failure correlation and rollout follow-up (docs/product/f9-software-lifecycle-design.md).
-- A Deployment gets the flag create_tasks (opt-out of the follow-up Tasks and notifications). A Turaco-derived Endpoint
-- Finding deployment_failure_cluster has a Deployment as its subject: failed or expired targets that share an error code,
-- device model, manufacturer, OS version or ring. deployment_followups records which follow-up Task was created for a
-- Deployment, reason and ring so the creation is idempotent.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

ALTER TABLE endpoints.deployments ADD COLUMN IF NOT EXISTS create_tasks boolean NOT NULL DEFAULT true;
-- last_correlated_at orders the correlation job: the least recently correlated Deployments go first, so a large backlog
-- is covered over several runs.
ALTER TABLE endpoints.deployments ADD COLUMN IF NOT EXISTS last_correlated_at timestamptz;
CREATE INDEX IF NOT EXISTS deployments_last_correlated_idx ON endpoints.deployments (last_correlated_at NULLS FIRST, id);

-- A finding has exactly one subject: a Device, a Software Package or a Deployment. cluster_key (dimension and value) tells
-- the clusters of one Deployment apart; at most one is open per Deployment and key.
ALTER TABLE endpoints.findings ADD COLUMN IF NOT EXISTS deployment_id uuid;
ALTER TABLE endpoints.findings ADD COLUMN IF NOT EXISTS cluster_key text;
-- below_runs counts the consecutive correlation runs in which an open cluster was below the threshold; the cluster is
-- resolved after the third (hysteresis), so a flapping group does not raise and resolve in turn.
ALTER TABLE endpoints.findings ADD COLUMN IF NOT EXISTS below_runs smallint NOT NULL DEFAULT 0;
ALTER TABLE endpoints.findings DROP CONSTRAINT IF EXISTS findings_deployment_fk;
ALTER TABLE endpoints.findings ADD CONSTRAINT findings_deployment_fk FOREIGN KEY (deployment_id)
    REFERENCES endpoints.deployments(id) ON DELETE CASCADE NOT VALID;
ALTER TABLE endpoints.findings VALIDATE CONSTRAINT findings_deployment_fk;

ALTER TABLE endpoints.findings DROP CONSTRAINT IF EXISTS findings_kind_check;
ALTER TABLE endpoints.findings ADD CONSTRAINT findings_kind_check
    CHECK (kind IN ('no_asset_match', 'serial_conflict', 'duplicate_device', 'unmatched_software', 'provider_reported_error', 'assignment_ineffective',
        'package_hash_mismatch', 'package_published_after_revoke', 'deployment_evidence_conflict', 'deployment_clear_failed',
        'deployment_failure_cluster')) NOT VALID;
ALTER TABLE endpoints.findings VALIDATE CONSTRAINT findings_kind_check;

ALTER TABLE endpoints.findings DROP CONSTRAINT IF EXISTS findings_subject_check;
ALTER TABLE endpoints.findings ADD CONSTRAINT findings_subject_check
    CHECK (num_nonnulls(device_id, software_package_id, deployment_id) = 1
        AND (kind IN ('package_hash_mismatch', 'package_published_after_revoke')) = (software_package_id IS NOT NULL)
        AND (kind = 'deployment_failure_cluster') = (deployment_id IS NOT NULL)
        AND (kind = 'deployment_failure_cluster') = (cluster_key IS NOT NULL)
        AND (cluster_key IS NULL OR length(cluster_key) BETWEEN 3 AND 160)) NOT VALID;
ALTER TABLE endpoints.findings VALIDATE CONSTRAINT findings_subject_check;

CREATE UNIQUE INDEX IF NOT EXISTS findings_deployment_one_open ON endpoints.findings (kind, deployment_id, cluster_key)
    WHERE status = 'open' AND deployment_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS findings_deployment_idx ON endpoints.findings (deployment_id, id) WHERE deployment_id IS NOT NULL;

-- One follow-up Task per Deployment, reason code and ring (ring_id NULL: the reason concerns the whole Deployment).
-- task_id is the Task created through the Tasks contract (no foreign key across modules).
CREATE TABLE IF NOT EXISTS endpoints.deployment_followups (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    deployment_id uuid NOT NULL REFERENCES endpoints.deployments(id) ON DELETE RESTRICT,
    reason text NOT NULL CHECK (reason ~ '^[a-z][a-z_]{0,39}$'),
    ring_id uuid,
    task_id uuid NOT NULL,
    -- unassigned: the owner was not an active user with deployments read access, the Task has no assignee.
    unassigned boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT deployment_followups_ring_fk FOREIGN KEY (ring_id, deployment_id) REFERENCES endpoints.deployment_rings (id, deployment_id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX IF NOT EXISTS deployment_followups_unique ON endpoints.deployment_followups
    (deployment_id, reason, COALESCE(ring_id, '00000000-0000-0000-0000-000000000000'::uuid));

DROP TRIGGER IF EXISTS deployment_followups_no_delete ON endpoints.deployment_followups;
CREATE TRIGGER deployment_followups_no_delete BEFORE DELETE ON endpoints.deployment_followups
    FOR EACH ROW EXECUTE FUNCTION endpoints.forbid_deployment_history_change();
DROP TRIGGER IF EXISTS deployment_followups_no_truncate ON endpoints.deployment_followups;
CREATE TRIGGER deployment_followups_no_truncate BEFORE TRUNCATE ON endpoints.deployment_followups
    FOR EACH STATEMENT EXECUTE FUNCTION endpoints.forbid_deployment_history_change();

-- Reports read the targets of a Deployment by state, time and ring; the failure groups by observation.
CREATE INDEX IF NOT EXISTS deployment_targets_deployment_state_idx ON endpoints.deployment_targets (deployment_id, state);
