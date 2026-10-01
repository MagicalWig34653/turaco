-- Job runner support (ADR-0006). Retryable failures return to 'pending' with a
-- later available_at; 'failed' is terminal (attempts exhausted or permanent).
ALTER TABLE platform.jobs ADD COLUMN IF NOT EXISTS dedupe_key text;

-- At most one pending/processing job per dedupe key (scheduled and manual
-- enqueues of the same work collapse into one).
CREATE UNIQUE INDEX IF NOT EXISTS jobs_active_dedupe_unique
    ON platform.jobs(dedupe_key)
    WHERE dedupe_key IS NOT NULL AND status IN ('pending', 'processing');

-- Scheduling looks up the most recent job per dedupe key.
CREATE INDEX IF NOT EXISTS jobs_dedupe_created_idx
    ON platform.jobs(dedupe_key, created_at DESC)
    WHERE dedupe_key IS NOT NULL;

-- Claiming considers pending jobs and stale processing jobs.
DROP INDEX IF EXISTS platform.jobs_claim_idx;
CREATE INDEX IF NOT EXISTS jobs_claim_idx
    ON platform.jobs(status, available_at, created_at)
    WHERE status IN ('pending', 'processing');

ALTER TABLE platform.jobs
    ADD CONSTRAINT jobs_job_type_not_empty CHECK (job_type <> ''),
    ADD CONSTRAINT jobs_dedupe_key_not_empty CHECK (dedupe_key IS NULL OR dedupe_key <> ''),
    ADD CONSTRAINT jobs_processing_locked CHECK (status <> 'processing' OR (locked_at IS NOT NULL AND locked_by IS NOT NULL));
