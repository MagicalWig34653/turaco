-- Manual IT Briefing items (F2 slice 6). A briefing item is operational
-- information highlighted to IT staff; items that reference an underlying
-- record arrive with F8. Plain text only: the body is never rendered as HTML.
CREATE TABLE IF NOT EXISTS briefing.items (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    title text NOT NULL CHECK (btrim(title) <> ''),
    body text NOT NULL DEFAULT '',
    severity text NOT NULL DEFAULT 'info' CHECK (severity IN ('info', 'warning', 'critical')),
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published', 'withdrawn')),
    -- After this instant a published item is no longer shown to viewers.
    valid_until timestamptz,
    author_user_id uuid,
    published_at timestamptz,
    published_by_user_id uuid,
    withdrawn_at timestamptz,
    withdrawn_by_user_id uuid,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT briefing_items_published_matches_status CHECK ((status = 'draft') = (published_at IS NULL)),
    CONSTRAINT briefing_items_withdrawn_matches_status CHECK ((status = 'withdrawn') = (withdrawn_at IS NOT NULL))
);
-- Shared list order: published items by publication time, drafts by creation time.
CREATE INDEX IF NOT EXISTS briefing_items_order_idx
    ON briefing.items ((coalesce(published_at, created_at)) DESC, id DESC);
CREATE INDEX IF NOT EXISTS briefing_items_published_idx
    ON briefing.items ((coalesce(published_at, created_at)) DESC, id DESC) WHERE status = 'published';
