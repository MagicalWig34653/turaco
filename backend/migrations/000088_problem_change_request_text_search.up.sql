-- Partial-word search for Problems, Changes and Service Requests (global search). Their searchers used to read the
-- newest 200 authorized records and match in memory; they now match in SQL with keyset paging, so older records are
-- found too. Words match as substrings of the folded reference and title (German umlauts and the sharp s folded,
-- like the knowledge search of 000083), served by trigram expression indexes (pg_trgm exists since 000060).
-- platform.fold_de is IMMUTABLE so it can back the expression indexes; it is the same fold as knowledge.fold_de,
-- kept in the platform schema so that no module reads another module's function.
-- Forward-only; index creation takes a share lock on each table for its duration.
CREATE OR REPLACE FUNCTION platform.fold_de(t text) RETURNS text
LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT AS
$$ SELECT replace(translate(lower(t), 'äöü', 'aou'), 'ß', 'ss') $$;

CREATE INDEX IF NOT EXISTS problems_reference_fold_trgm_idx ON servicedesk.problems USING gin (platform.fold_de(reference) gin_trgm_ops);
CREATE INDEX IF NOT EXISTS problems_title_fold_trgm_idx ON servicedesk.problems USING gin (platform.fold_de(title) gin_trgm_ops);
CREATE INDEX IF NOT EXISTS changes_reference_fold_trgm_idx ON changes.changes USING gin (platform.fold_de(reference) gin_trgm_ops);
CREATE INDEX IF NOT EXISTS changes_title_fold_trgm_idx ON changes.changes USING gin (platform.fold_de(title) gin_trgm_ops);
CREATE INDEX IF NOT EXISTS service_requests_reference_fold_trgm_idx ON requests.service_requests USING gin (platform.fold_de(reference) gin_trgm_ops);
CREATE INDEX IF NOT EXISTS service_requests_title_fold_trgm_idx ON requests.service_requests USING gin (platform.fold_de(catalog_item_title) gin_trgm_ops);
