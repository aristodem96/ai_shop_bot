-- +goose Up

-- Auto-DB names a brand by its supplier id; a crawl brings the names, and
-- they are kept to tell whose article a search hit is.
CREATE TABLE autodb_suppliers (
    id       bigint PRIMARY KEY,
    name     text   NOT NULL,
    name_key text   NOT NULL
);
CREATE INDEX autodb_suppliers_key_idx ON autodb_suppliers (name_key);
INSERT INTO autodb_suppliers (id, name, name_key)
SELECT DISTINCT ON (supplier_id) supplier_id, brand, brand_key FROM autodb_mod_parts
ON CONFLICT DO NOTHING;

-- What Auto-DB knows of an article (getArtInfo), fetched once: its photos,
-- characteristics, the carmakers' numbers it replaces, and the same part
-- from other brands.
CREATE TABLE autodb_articles (
    supplier_id bigint NOT NULL,
    article_key text   NOT NULL,
    number      text   NOT NULL,
    name        text   NOT NULL DEFAULT '',
    specs       jsonb  NOT NULL DEFAULT '[]',
    images      text[] NOT NULL DEFAULT '{}',
    oem_keys    text[] NOT NULL DEFAULT '{}',
    fetched_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (supplier_id, article_key)
);

CREATE TABLE autodb_crosses (
    supplier_id bigint NOT NULL,
    article_key text   NOT NULL,
    brand_key   text   NOT NULL,
    cross_key   text   NOT NULL,
    PRIMARY KEY (supplier_id, article_key, brand_key, cross_key)
);
CREATE INDEX autodb_crosses_cross_idx ON autodb_crosses (cross_key);

-- A product matched to its Auto-DB article, and when it was looked for:
-- wanted_at puts a product a buyer opened ahead of the background pass.
ALTER TABLE products
    ADD COLUMN autodb_supplier_id bigint,
    ADD COLUMN autodb_checked_at  timestamptz,
    ADD COLUMN autodb_wanted_at   timestamptz;
CREATE INDEX products_autodb_todo_idx ON products (autodb_wanted_at DESC NULLS LAST, id)
    WHERE autodb_checked_at IS NULL AND part_key <> '';

-- A photo Telegram already has, by its source url: sent again by file_id,
-- with no download, conversion or upload.
CREATE TABLE photo_files (
    url        text PRIMARY KEY,
    file_id    text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down

DROP TABLE photo_files;
DROP INDEX products_autodb_todo_idx;
ALTER TABLE products DROP COLUMN autodb_wanted_at, DROP COLUMN autodb_checked_at, DROP COLUMN autodb_supplier_id;
DROP TABLE autodb_crosses;
DROP TABLE autodb_articles;
DROP TABLE autodb_suppliers;
