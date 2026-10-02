-- In-app notifications (F2 slice 3, ADR-0024). One row per recipient; the
-- text is localized by the client from category and params. Idempotent
-- creation by consumers uses (recipient_user_id, dedupe_key).
CREATE TABLE IF NOT EXISTS platform.notifications (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    recipient_user_id uuid NOT NULL,
    category text NOT NULL CHECK (category <> ''),
    params jsonb NOT NULL DEFAULT '{}'::jsonb,
    link_type text,
    link_id uuid,
    dedupe_key text NOT NULL CHECK (dedupe_key <> ''),
    created_at timestamptz NOT NULL DEFAULT now(),
    read_at timestamptz,
    CONSTRAINT notifications_link_pair CHECK ((link_type IS NULL) = (link_id IS NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS notifications_dedupe_unique
    ON platform.notifications(recipient_user_id, dedupe_key);
CREATE INDEX IF NOT EXISTS notifications_recipient_idx
    ON platform.notifications(recipient_user_id, id DESC);
CREATE INDEX IF NOT EXISTS notifications_unread_idx
    ON platform.notifications(recipient_user_id, id DESC) WHERE read_at IS NULL;
