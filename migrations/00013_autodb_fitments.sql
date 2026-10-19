-- +goose Up

-- What fits a modification, as Auto-DB lists it: every part of every
-- section of its parts tree, a brand's article each. Crawled in the
-- background when a buyer chooses the car, kept, and recrawled after a
-- while. article_key and brand_key are letters and digits in upper case,
-- the way the shop's own keys are written.
CREATE TABLE autodb_mod_parts (
    mod_id      bigint NOT NULL,
    section_id  bigint NOT NULL,
    supplier_id bigint NOT NULL,
    article_key text   NOT NULL,
    number      text   NOT NULL,
    brand       text   NOT NULL,
    brand_key   text   NOT NULL,
    name        text   NOT NULL DEFAULT '',
    PRIMARY KEY (mod_id, section_id, supplier_id, article_key)
);
CREATE INDEX autodb_mod_parts_article_idx ON autodb_mod_parts (article_key);

-- The crawl of one modification: queued when a buyer picks the car, then
-- running, then done (or failed, to be retried).
CREATE TABLE autodb_crawls (
    mod_id         bigint PRIMARY KEY,
    status         text   NOT NULL CHECK (status IN ('queued', 'running', 'done', 'failed')),
    sections_total int    NOT NULL DEFAULT 0,
    sections_done  int    NOT NULL DEFAULT 0,
    parts          int    NOT NULL DEFAULT 0,
    error          text   NOT NULL DEFAULT '',
    queued_at      timestamptz NOT NULL DEFAULT now(),
    finished_at    timestamptz
);
CREATE INDEX autodb_crawls_queue_idx ON autodb_crawls (queued_at) WHERE status = 'queued';

-- A product's own article, the key it meets Auto-DB's lists by: the
-- supplier's part number, normalised. Set by the import; filled here for
-- what was imported before.
ALTER TABLE products
    ADD COLUMN part_key  text NOT NULL DEFAULT '',
    ADD COLUMN brand_key text GENERATED ALWAYS AS (upper(regexp_replace(brand, '[^[:alnum:]]', '', 'g'))) STORED;
UPDATE products SET part_key = oem_numbers[1] WHERE source <> '' AND cardinality(oem_numbers) > 0;
CREATE INDEX products_part_key_idx ON products (part_key) WHERE part_key <> '';

-- Fitments now have a third source: a product fits an Auto-DB vehicle when
-- its article is in the vehicle's crawled list, from the same brand -- or,
-- for a price list that names no brand, by the article alone. A brand the
-- catalogue spells otherwise ("MANN-FILTER" for MANN) meets through the
-- brand aliases. Every query that asks vehicle_fitments sees it.
CREATE OR REPLACE VIEW vehicle_fitments AS
SELECT f.product_id, f.vehicle_id, f.position, f.note, true AS direct
FROM product_fitments f
UNION ALL
SELECT mf.product_id, v.id, mf.position, mf.note, false
FROM product_model_fitments mf
JOIN vehicle_models m ON m.id = mf.model_id
JOIN vehicles v ON v.make = m.make AND v.model = m.model
WHERE (mf.year_from IS NULL OR coalesce(v.year_to, 9999) >= mf.year_from)
  AND (mf.year_to IS NULL OR v.year_from <= mf.year_to)
UNION ALL
SELECT DISTINCT p.id, v.id, '', '', false
FROM vehicles v
JOIN autodb_mod_parts mp ON mp.mod_id = v.autodb_mod_id
JOIN products p ON p.part_key = mp.article_key AND p.part_key <> ''
WHERE p.brand = ''
   OR p.brand_key = mp.brand_key
   OR EXISTS (SELECT 1 FROM brand_aliases a JOIN brands b ON b.id = a.brand_id
              WHERE a.alias = mp.brand_key AND b.name = p.brand);

-- +goose Down

CREATE OR REPLACE VIEW vehicle_fitments AS
SELECT f.product_id, f.vehicle_id, f.position, f.note, true AS direct
FROM product_fitments f
UNION ALL
SELECT mf.product_id, v.id, mf.position, mf.note, false
FROM product_model_fitments mf
JOIN vehicle_models m ON m.id = mf.model_id
JOIN vehicles v ON v.make = m.make AND v.model = m.model
WHERE (mf.year_from IS NULL OR coalesce(v.year_to, 9999) >= mf.year_from)
  AND (mf.year_to IS NULL OR v.year_from <= mf.year_to);
DROP INDEX products_part_key_idx;
ALTER TABLE products DROP COLUMN brand_key, DROP COLUMN part_key;
DROP TABLE autodb_crawls;
DROP TABLE autodb_mod_parts;
