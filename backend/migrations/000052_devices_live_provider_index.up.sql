-- Endpoints (F9 G2 review): Target Set evaluation pages through a provider's live Devices in id order and counts
-- them for the high-impact share; this partial index serves both. The migrator wraps this file in a transaction.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '120s';

CREATE INDEX IF NOT EXISTS devices_live_provider_idx ON endpoints.devices (provider, id) WHERE deleted_observed_at IS NULL;
