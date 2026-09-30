CREATE TABLE IF NOT EXISTS products.manufacturers (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    name text NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS products.categories (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    name text NOT NULL,
    parent_category_id uuid REFERENCES products.categories(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS products.products (
    id uuid PRIMARY KEY DEFAULT uuidv7(),
    name text NOT NULL,
    manufacturer_id uuid REFERENCES products.manufacturers(id),
    category_id uuid REFERENCES products.categories(id),
    manufacturer_part_number text,
    internal_part_number text,
    serialized boolean NOT NULL DEFAULT false,
    stock_managed boolean NOT NULL DEFAULT true,
    asset_managed boolean NOT NULL DEFAULT false,
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS products_name_idx ON products.products(lower(name));
