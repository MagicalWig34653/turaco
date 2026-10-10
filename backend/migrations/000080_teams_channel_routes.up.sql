-- Microsoft Teams channel posts (F15 slice T-A, ADR-0036).
--
-- A Channel Route maps a broadcastable notification category to a Teams Channel Destination key. The key names an
-- entry of the deployment secret file TEAMS_CHANNEL_DESTINATIONS_FILE; the database never holds a webhook URL.
CREATE TABLE IF NOT EXISTS platform.notification_channel_routes (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    channel text NOT NULL CHECK (channel IN ('teams_channel')),
    category text NOT NULL CHECK (category <> ''),
    destination_key text NOT NULL CHECK (destination_key ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    created_by uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT notification_channel_routes_unique UNIQUE (channel, category, destination_key)
);

-- A channel post is a delivery without a recipient: one per (event, destination). It has no notification, so the
-- notification reference becomes optional, and it carries the reference-only content it will render (category, kind,
-- reference number, link type and id) plus the idempotency key of the event.
ALTER TABLE platform.notification_deliveries ALTER COLUMN notification_id DROP NOT NULL;
ALTER TABLE platform.notification_deliveries ADD COLUMN IF NOT EXISTS destination_key text;
ALTER TABLE platform.notification_deliveries ADD COLUMN IF NOT EXISTS dedupe_key text;
ALTER TABLE platform.notification_deliveries ADD COLUMN IF NOT EXISTS payload jsonb;

ALTER TABLE platform.notification_deliveries DROP CONSTRAINT IF EXISTS notification_deliveries_channel_check;
ALTER TABLE platform.notification_deliveries ADD CONSTRAINT notification_deliveries_channel_check
    CHECK (channel IN ('email', 'teams_channel'));
ALTER TABLE platform.notification_deliveries DROP CONSTRAINT IF EXISTS notification_deliveries_shape;
ALTER TABLE platform.notification_deliveries ADD CONSTRAINT notification_deliveries_shape CHECK (
    (channel = 'email' AND notification_id IS NOT NULL AND destination_key IS NULL AND dedupe_key IS NULL AND payload IS NULL)
    OR (channel = 'teams_channel' AND notification_id IS NULL AND destination_key IS NOT NULL
        AND dedupe_key IS NOT NULL AND payload IS NOT NULL));

-- At most one post per event and destination, however often the producer's consumer runs.
CREATE UNIQUE INDEX IF NOT EXISTS notification_deliveries_post_dedupe
    ON platform.notification_deliveries(channel, destination_key, dedupe_key) WHERE dedupe_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS notification_deliveries_channel_time_idx
    ON platform.notification_deliveries(channel, created_at);
