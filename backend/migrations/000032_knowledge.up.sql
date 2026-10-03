-- Knowledge (F5 slice 3): articles with an audience and a lifecycle, searchable with
-- PostgreSQL full-text search (no extra dependency).
CREATE SCHEMA IF NOT EXISTS knowledge;

CREATE SEQUENCE IF NOT EXISTS knowledge.article_number_seq;

CREATE OR REPLACE FUNCTION knowledge.next_reference() RETURNS text
LANGUAGE sql
AS $$
    SELECT 'KB-' || CASE WHEN n < 1000000 THEN lpad(n::text, 6, '0') ELSE n::text END
    FROM (SELECT nextval('knowledge.article_number_seq') AS n) AS s
$$;

CREATE TABLE IF NOT EXISTS knowledge.articles (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    reference text NOT NULL DEFAULT knowledge.next_reference(),
    title text NOT NULL CHECK (title = btrim(title) AND length(title) BETWEEN 1 AND 200),
    summary text NOT NULL DEFAULT '' CHECK (length(summary) <= 500),
    body text NOT NULL CHECK (length(body) BETWEEN 1 AND 20000),
    -- internal: IT staff only; employee: every signed-in user once published.
    audience text NOT NULL DEFAULT 'internal' CHECK (audience IN ('internal', 'employee')),
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published', 'retired')),
    author_user_id uuid,
    published_at timestamptz,
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    search tsvector GENERATED ALWAYS AS (
        setweight(to_tsvector('simple', title), 'A') || setweight(to_tsvector('simple', summary), 'B') || setweight(to_tsvector('simple', body), 'C')
    ) STORED,
    CONSTRAINT articles_reference_unique UNIQUE (reference),
    CONSTRAINT articles_published_matches CHECK ((status = 'draft') = (published_at IS NULL))
);
CREATE INDEX IF NOT EXISTS articles_search_idx ON knowledge.articles USING gin (search);
CREATE INDEX IF NOT EXISTS articles_status_idx ON knowledge.articles (status, id DESC);
