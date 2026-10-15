-- +goose Up

CREATE TABLE categories (
    id       bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    slug     text NOT NULL UNIQUE,
    title    text NOT NULL,
    position int  NOT NULL DEFAULT 0
);

-- A size chart maps a size label to body measurements. Charts are shared by
-- many products (every T-shirt uses "tops"), so they live in their own table
-- instead of being copied onto each product. Stage 4 (size advice) reads them;
-- every measurement column is nullable because a chart only fills the ones
-- that make sense for it: chest for tops, waist for jeans, foot for shoes.
CREATE TABLE size_charts (
    id    bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code  text NOT NULL UNIQUE,
    title text NOT NULL
);

CREATE TABLE size_chart_rows (
    chart_id      bigint NOT NULL REFERENCES size_charts (id) ON DELETE CASCADE,
    size          text   NOT NULL,
    -- Sizes do not sort alphabetically (XS < S < M < L), so the order is stored.
    position      int    NOT NULL,
    chest_min_cm  int,
    chest_max_cm  int,
    waist_min_cm  int,
    waist_max_cm  int,
    height_min_cm int,
    height_max_cm int,
    foot_min_mm   int,
    foot_max_mm   int,
    PRIMARY KEY (chart_id, size)
);

-- One row is one model in one colour, the way shops number SKUs. Colour is
-- therefore a plain column rather than a variant axis, which keeps "not black"
-- a simple WHERE clause for the stage 2 agent.
CREATE TABLE products (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    sku           text   NOT NULL UNIQUE,
    category_id   bigint NOT NULL REFERENCES categories (id),
    size_chart_id bigint NOT NULL REFERENCES size_charts (id),
    name          text   NOT NULL,
    brand         text   NOT NULL,
    description   text   NOT NULL DEFAULT '',
    color         text   NOT NULL,
    season        text   NOT NULL CHECK (season IN ('summer', 'demi', 'winter', 'all')),
    gender        text   NOT NULL CHECK (gender IN ('men', 'women', 'unisex')),
    styles        text[] NOT NULL DEFAULT '{}',
    -- In kopiyky. Monobank takes amounts in minor units too, so money never
    -- passes through a float on its way to the payment provider.
    price         bigint NOT NULL CHECK (price > 0),
    image_url     text   NOT NULL,
    is_active     boolean NOT NULL DEFAULT true,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    -- 'simple' rather than a language config: Postgres ships no Ukrainian
    -- stemmer, and a wrong one (russian) would mangle Ukrainian words worse
    -- than no stemming at all. Prefix queries cover most of the gap.
    search        tsvector GENERATED ALWAYS AS (
        setweight(to_tsvector('simple', name), 'A') ||
        setweight(to_tsvector('simple', brand), 'A') ||
        setweight(to_tsvector('simple', description), 'B')
    ) STORED
);

CREATE INDEX products_category_idx ON products (category_id, price) WHERE is_active;
CREATE INDEX products_search_idx ON products USING gin (search);

-- Stock is kept per size. The CHECK is the last line of defence: whatever the
-- application gets wrong, the database refuses to sell a unit it does not have.
CREATE TABLE product_variants (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    product_id bigint NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    size       text   NOT NULL,
    position   int    NOT NULL,
    stock      int    NOT NULL CHECK (stock >= 0),
    UNIQUE (product_id, size)
);

-- Keyed by the Telegram user id: the bot only works in private chats, and a
-- user has exactly one cart. reminded_at is for the abandoned cart reminder,
-- which must fire once, not on every scan.
CREATE TABLE carts (
    user_id     bigint PRIMARY KEY,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    reminded_at timestamptz
);

-- A cart line holds no price: the price is always read from products, so a
-- change in the catalogue is what the buyer sees, and nothing in a cart can be
-- used to buy at a stale price.
CREATE TABLE cart_items (
    user_id    bigint NOT NULL REFERENCES carts (user_id) ON DELETE CASCADE,
    variant_id bigint NOT NULL REFERENCES product_variants (id) ON DELETE CASCADE,
    qty        int    NOT NULL CHECK (qty > 0),
    added_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, variant_id)
);

CREATE INDEX cart_items_variant_idx ON cart_items (variant_id);

-- +goose Down

DROP TABLE cart_items;
DROP TABLE carts;
DROP TABLE product_variants;
DROP TABLE products;
DROP TABLE size_chart_rows;
DROP TABLE size_charts;
DROP TABLE categories;
