-- +goose Up

CREATE TABLE categories (
    id       bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    slug     text NOT NULL UNIQUE,
    title    text NOT NULL,
    position int  NOT NULL DEFAULT 0
);

-- A vehicle is one make, model, generation and engine: the level at which
-- parts differ. A 1.4 TSI and a 2.0 TDI Octavia of the same year take
-- different oil filters, so they are two rows. year_to is NULL while the model
-- is still produced. code is the natural key the seed upserts on.
CREATE TABLE vehicles (
    id        bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code      text NOT NULL UNIQUE,
    make      text NOT NULL,
    model     text NOT NULL,
    year_from int  NOT NULL,
    year_to   int,
    engine    text NOT NULL,
    fuel      text NOT NULL CHECK (fuel IN ('petrol', 'diesel', 'hybrid', 'electric')),
    CHECK (year_to IS NULL OR year_to >= year_from)
);

CREATE INDEX vehicles_make_idx ON vehicles (make, model, year_from);

CREATE TABLE products (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    sku         text   NOT NULL UNIQUE,
    category_id bigint NOT NULL REFERENCES categories (id),
    name        text   NOT NULL,
    brand       text   NOT NULL,
    description text   NOT NULL DEFAULT '',
    -- Original equipment numbers, normalised (upper case, no spaces, dots or
    -- dashes). Two parts sharing one are interchangeable, which is what the
    -- "analogues" button and part-number search are built on.
    oem_numbers text[] NOT NULL DEFAULT '{}',
    -- Display-only characteristics: [{"name": "Вісь", "value": "передня"}].
    -- An ordered array rather than an object so the card shows them in the
    -- order they were written.
    specs       jsonb  NOT NULL DEFAULT '[]',
    -- A universal part (oil, a bulb, a battery) fits every vehicle and has no
    -- fitment rows. The flag keeps "fits my car" a single condition instead
    -- of fitment rows for every vehicle ever added.
    universal   boolean NOT NULL DEFAULT false,
    -- In kopiyky. Monobank takes amounts in minor units too, so money never
    -- passes through a float on its way to the payment provider.
    price       bigint NOT NULL CHECK (price > 0),
    -- The CHECK is the last line of defence: whatever the application gets
    -- wrong, the database refuses to sell a unit it does not have.
    stock       int    NOT NULL CHECK (stock >= 0),
    image_url   text   NOT NULL,
    is_active   boolean NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    -- 'simple' rather than a language config: Postgres ships no Ukrainian
    -- stemmer, and a wrong one (russian) would mangle Ukrainian words worse
    -- than no stemming at all. Prefix queries cover most of the gap.
    search      tsvector GENERATED ALWAYS AS (
        setweight(to_tsvector('simple', name), 'A') ||
        setweight(to_tsvector('simple', brand), 'A') ||
        setweight(to_tsvector('simple', description), 'B')
    ) STORED
);

CREATE INDEX products_category_idx ON products (category_id, price) WHERE is_active;
CREATE INDEX products_search_idx ON products USING gin (search);
CREATE INDEX products_oem_idx ON products USING gin (oem_numbers);

-- Which vehicles a part fits. The primary key serves "parts for my car" from
-- the vehicle side through the second index.
CREATE TABLE product_fitments (
    product_id bigint NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    vehicle_id bigint NOT NULL REFERENCES vehicles (id) ON DELETE CASCADE,
    PRIMARY KEY (product_id, vehicle_id)
);

CREATE INDEX product_fitments_vehicle_idx ON product_fitments (vehicle_id, product_id);

-- The buyer's car, keyed by Telegram user id. One car per user: the catalogue
-- filters by it, and a second car is chosen by switching.
CREATE TABLE garages (
    user_id    bigint PRIMARY KEY,
    vehicle_id bigint NOT NULL REFERENCES vehicles (id) ON DELETE CASCADE,
    updated_at timestamptz NOT NULL DEFAULT now()
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
    product_id bigint NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    qty        int    NOT NULL CHECK (qty > 0),
    added_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, product_id)
);

CREATE INDEX cart_items_product_idx ON cart_items (product_id);

-- +goose Down

DROP TABLE cart_items;
DROP TABLE carts;
DROP TABLE garages;
DROP TABLE product_fitments;
DROP TABLE products;
DROP TABLE vehicles;
DROP TABLE categories;
