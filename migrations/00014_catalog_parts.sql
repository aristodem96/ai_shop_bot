-- +goose Up

-- A catalogue part a buyer can ask for is named by a button, and buttons
-- carry numbers: each crawled row gets one. A recrawl replaces the rows and
-- their numbers, which turns an old button into "no longer listed", never
-- into another part.
ALTER TABLE autodb_mod_parts ADD COLUMN id bigint GENERATED ALWAYS AS IDENTITY UNIQUE;

-- Auto-DB names parts in Russian, and a few hundred names cover the whole
-- catalogue. Each is translated once, by a model, together with the shop's
-- category it belongs in, and kept.
CREATE TABLE autodb_part_names (
    name       text PRIMARY KEY,
    name_uk    text NOT NULL,
    category   text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX autodb_mod_parts_name_idx ON autodb_mod_parts (name);

-- +goose Down

DROP INDEX autodb_mod_parts_name_idx;
DROP TABLE autodb_part_names;
ALTER TABLE autodb_mod_parts DROP COLUMN id;
