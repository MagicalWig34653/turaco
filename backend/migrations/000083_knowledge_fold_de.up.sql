-- Folds German umlauts and the sharp s for the knowledge substring search ("Gast" finds "Gäste"). IMMUTABLE so it
-- can later back an expression index; a plain scan is enough for the small article corpus.
CREATE OR REPLACE FUNCTION knowledge.fold_de(t text) RETURNS text
LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT AS
$$ SELECT replace(translate(lower(t), 'äöü', 'aou'), 'ß', 'ss') $$;
