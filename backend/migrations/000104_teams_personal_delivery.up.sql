-- Personal Microsoft Teams cards (F15 slice T-B, ADR-0036): a delivery of channel 'teams' belongs to a notification
-- like an email delivery, and a User can opt out of it per category.
ALTER TABLE platform.notification_deliveries DROP CONSTRAINT IF EXISTS notification_deliveries_channel_check;
ALTER TABLE platform.notification_deliveries ADD CONSTRAINT notification_deliveries_channel_check
    CHECK (channel IN ('email', 'teams_channel', 'teams'));
ALTER TABLE platform.notification_deliveries DROP CONSTRAINT IF EXISTS notification_deliveries_shape;
ALTER TABLE platform.notification_deliveries ADD CONSTRAINT notification_deliveries_shape CHECK (
    (channel IN ('email', 'teams') AND notification_id IS NOT NULL AND destination_key IS NULL AND dedupe_key IS NULL AND payload IS NULL)
    OR (channel = 'teams_channel' AND notification_id IS NULL AND destination_key IS NOT NULL
        AND dedupe_key IS NOT NULL AND payload IS NOT NULL));

ALTER TABLE platform.notification_preferences DROP CONSTRAINT IF EXISTS notification_preferences_channel_check;
ALTER TABLE platform.notification_preferences ADD CONSTRAINT notification_preferences_channel_check
    CHECK (channel IN ('email', 'teams'));
