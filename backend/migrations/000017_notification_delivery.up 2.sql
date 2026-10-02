-- Email channel of the Notification service (F2 slice 4, ADR-0024).
-- A delivery is the state of sending one notification through one channel
-- (docs/domain/state-machines.md, "NotificationDelivery"); it is separate from
-- the notification itself.
CREATE TABLE IF NOT EXISTS platform.notification_deliveries (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    notification_id uuid NOT NULL REFERENCES platform.notifications(id) ON DELETE CASCADE,
    channel text NOT NULL CHECK (channel IN ('email')),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sending', 'delivered', 'failed', 'cancelled')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz,
    CONSTRAINT notification_deliveries_unique UNIQUE (notification_id, channel),
    CONSTRAINT notification_deliveries_delivered_time CHECK ((status = 'delivered') = (delivered_at IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS notification_deliveries_open_idx
    ON platform.notification_deliveries(status) WHERE status IN ('pending', 'sending');

-- A User's opt-out per category and channel; no row means enabled.
CREATE TABLE IF NOT EXISTS platform.notification_preferences (
    user_id uuid NOT NULL,
    category text NOT NULL CHECK (category <> ''),
    channel text NOT NULL CHECK (channel IN ('email')),
    enabled boolean NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, category, channel)
);
