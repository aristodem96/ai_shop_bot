-- +goose Up

-- What a real shop's price list looks like decides this migration: one row per
-- offer, a vehicle named only as "Make Model", the same part number under
-- several brands, brands spelled five ways, no photo. The tables below let
-- such a file be loaded without inventing data it does not have.

-- A model without generation details or engine: "Skoda Octavia A7". It is the
-- level most price lists stop at. vehicles keeps make and model as text (every
-- query reads them), and the composite key makes the database keep the two
-- in step: a vehicle cannot name a model that does not exist.
CREATE TABLE vehicle_models (
    id    bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    make  text NOT NULL,
    model text NOT NULL,
    UNIQUE (make, model)
);

INSERT INTO vehicle_models (make, model) SELECT DISTINCT make, model FROM vehicles;

ALTER TABLE vehicles
    ADD FOREIGN KEY (make, model) REFERENCES vehicle_models (make, model) ON UPDATE CASCADE;

-- Where on the car and under what conditions: "передня вісь", "лівий",
-- "з кондиціонером". Without it two different pads both "fit" the same car
-- and the buyer cannot tell which one they need.
ALTER TABLE product_fitments
    ADD COLUMN position text NOT NULL DEFAULT '',
    ADD COLUMN note     text NOT NULL DEFAULT '';

-- A fitment to a whole model, optionally within years. It applies to every
-- vehicle (engine) of the model whose production overlaps the years, and to
-- vehicles of that model added later, which expanding it into
-- product_fitments rows at import time would miss.
CREATE TABLE product_model_fitments (
    product_id bigint NOT NULL REFERENCES products (id) ON DELETE CASCADE,
    model_id   bigint NOT NULL REFERENCES vehicle_models (id) ON DELETE CASCADE,
    year_from  int,
    year_to    int,
    position   text NOT NULL DEFAULT '',
    note       text NOT NULL DEFAULT '',
    PRIMARY KEY (product_id, model_id),
    CHECK (year_from IS NULL OR year_to IS NULL OR year_to >= year_from)
);

CREATE INDEX product_model_fitments_model_idx ON product_model_fitments (model_id, product_id);

-- Every fitment resolved to concrete vehicles. Queries ask "does p fit
-- vehicle v" of this one view and need not know there are two kinds. A
-- product fitted both ways to the same vehicle appears twice; EXISTS does not
-- care, and the card query takes one row per vehicle, the direct one (the
-- more specific) by preference.
CREATE VIEW vehicle_fitments AS
SELECT f.product_id, f.vehicle_id, f.position, f.note, true AS direct
FROM product_fitments f
UNION ALL
SELECT mf.product_id, v.id, mf.position, mf.note, false
FROM product_model_fitments mf
JOIN vehicle_models m ON m.id = mf.model_id
JOIN vehicles v ON v.make = m.make AND v.model = m.model
WHERE (mf.year_from IS NULL OR coalesce(v.year_to, 9999) >= mf.year_from)
  AND (mf.year_to IS NULL OR v.year_from <= mf.year_to);

-- The brand directory. Price lists write one brand as "KYB", "Kyb", "K.Y.B."
-- or "Kayaba"; products.brand holds the canonical name, and every spelling an
-- import has met maps to it here, keyed by letters and digits in upper case.
-- Spellings that differ by more than punctuation ("Kayaba") are added by hand,
-- once, and every later import gets them right.
CREATE TABLE brands (
    id   bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name text NOT NULL UNIQUE
);

CREATE TABLE brand_aliases (
    alias    text PRIMARY KEY,
    brand_id bigint NOT NULL REFERENCES brands (id) ON DELETE CASCADE
);

INSERT INTO brands (name) SELECT DISTINCT brand FROM products WHERE brand <> '';
INSERT INTO brand_aliases (alias, brand_id)
SELECT upper(regexp_replace(name, '[^[:alnum:]]', '', 'g')), id FROM brands
ON CONFLICT DO NOTHING;

-- The supplier's own key for an offer. A price list has no SKU of ours, and
-- its part number is no key either: the same number is sold under several
-- brands. (source, external_id) is what a re-import matches on. Products
-- entered by hand or by the seed have neither.
ALTER TABLE products
    ADD COLUMN source      text NOT NULL DEFAULT '',
    ADD COLUMN external_id text,
    ADD CONSTRAINT products_source_external_key UNIQUE (source, external_id);

-- Most price lists have no photos. An empty URL means none, and the card goes
-- out as text.
ALTER TABLE products ALTER COLUMN image_url SET DEFAULT '';

-- +goose Down

ALTER TABLE products ALTER COLUMN image_url DROP DEFAULT;
ALTER TABLE products DROP CONSTRAINT products_source_external_key,
    DROP COLUMN external_id, DROP COLUMN source;
DROP TABLE brand_aliases;
DROP TABLE brands;
DROP VIEW vehicle_fitments;
DROP TABLE product_model_fitments;
ALTER TABLE product_fitments DROP COLUMN note, DROP COLUMN position;
ALTER TABLE vehicles DROP CONSTRAINT vehicles_make_model_fkey;
DROP TABLE vehicle_models;
