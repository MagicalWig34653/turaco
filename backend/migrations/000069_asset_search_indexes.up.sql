-- Asset, product and manufacturer search (change wizard, asset list): substring search needs trigram indexes
-- (ADR-0033). pg_trgm exists since migration 000060.
CREATE INDEX IF NOT EXISTS assets_reference_trgm_idx ON assets.assets USING gin (reference gin_trgm_ops);
CREATE INDEX IF NOT EXISTS assets_tag_trgm_idx ON assets.assets USING gin (asset_tag gin_trgm_ops);
CREATE INDEX IF NOT EXISTS assets_serial_trgm_idx ON assets.assets USING gin (serial_number gin_trgm_ops);
CREATE INDEX IF NOT EXISTS products_name_trgm_idx ON products.products USING gin (name gin_trgm_ops);
CREATE INDEX IF NOT EXISTS products_mpn_trgm_idx ON products.products USING gin (manufacturer_part_number gin_trgm_ops);
CREATE INDEX IF NOT EXISTS products_ipn_trgm_idx ON products.products USING gin (internal_part_number gin_trgm_ops);
CREATE INDEX IF NOT EXISTS manufacturers_name_trgm_idx ON products.manufacturers USING gin (name gin_trgm_ops);
