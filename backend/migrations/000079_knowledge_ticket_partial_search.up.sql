-- Platform search (F14 section 5): partial words. Full-text search finds whole words only, so "Etikett" did not find
-- "Etikettendrucker". Knowledge articles are now also matched as substrings of title, summary, body and reference
-- (ILIKE through trigram indexes; pg_trgm exists since migration 000060). The ticket text search already matches
-- substrings of reference, title and device text through the trigram indexes of 000060 and 000068; the platform
-- search uses that same query, so no ticket index is added.
-- Forward-only; index creation takes a share lock on knowledge.articles for its duration.
CREATE INDEX IF NOT EXISTS articles_title_trgm_idx ON knowledge.articles USING gin (title gin_trgm_ops);
CREATE INDEX IF NOT EXISTS articles_summary_trgm_idx ON knowledge.articles USING gin (summary gin_trgm_ops);
CREATE INDEX IF NOT EXISTS articles_body_trgm_idx ON knowledge.articles USING gin (body gin_trgm_ops);
CREATE INDEX IF NOT EXISTS articles_reference_trgm_idx ON knowledge.articles USING gin (reference gin_trgm_ops);
