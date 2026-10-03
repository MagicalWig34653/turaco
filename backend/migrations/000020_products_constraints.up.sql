-- Products module (F3 slice 1): optimistic versions and integrity rules on
-- the tables created in 000004. No product data existed before this slice.
ALTER TABLE products.manufacturers ADD COLUMN IF NOT EXISTS version integer NOT NULL DEFAULT 1;
ALTER TABLE products.categories ADD COLUMN IF NOT EXISTS version integer NOT NULL DEFAULT 1;
ALTER TABLE products.products ADD COLUMN IF NOT EXISTS version integer NOT NULL DEFAULT 1;

ALTER TABLE products.manufacturers
    ADD CONSTRAINT manufacturers_name_not_blank CHECK (btrim(name) <> ''),
    ADD CONSTRAINT manufacturers_version_positive CHECK (version > 0);
ALTER TABLE products.categories
    ADD CONSTRAINT categories_name_not_blank CHECK (btrim(name) <> ''),
    ADD CONSTRAINT categories_version_positive CHECK (version > 0);
ALTER TABLE products.products
    ADD CONSTRAINT products_name_not_blank CHECK (btrim(name) <> ''),
    ADD CONSTRAINT products_version_positive CHECK (version > 0);

-- Manufacturer names are unique case-insensitively (the existing UNIQUE is case-sensitive).
CREATE UNIQUE INDEX IF NOT EXISTS manufacturers_name_lower_unique ON products.manufacturers (lower(name));
-- Category names are unique per parent (root categories share the nil parent).
CREATE UNIQUE INDEX IF NOT EXISTS categories_parent_name_unique
    ON products.categories (coalesce(parent_category_id, '00000000-0000-0000-0000-000000000000'::uuid), lower(name));
CREATE UNIQUE INDEX IF NOT EXISTS products_internal_part_number_unique
    ON products.products (lower(internal_part_number)) WHERE internal_part_number IS NOT NULL;
CREATE INDEX IF NOT EXISTS products_category_idx ON products.products (category_id) WHERE category_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS products_manufacturer_idx ON products.products (manufacturer_id) WHERE manufacturer_id IS NOT NULL;
