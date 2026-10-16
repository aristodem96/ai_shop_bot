-- +goose Up

-- Six flat categories fit a demo; a real price list has bodywork, steering,
-- gearboxes and fasteners too, and a flat list of fifty is not a menu. The
-- tree is two levels: sections and their subcategories. It comes from
-- data/categories.csv, synced on start, with the keywords that file imported
-- rows into it and what a maintenance kit needs to know of its parts.
ALTER TABLE categories
    ADD COLUMN parent_id bigint REFERENCES categories (id),
    ADD COLUMN part_type text    NOT NULL DEFAULT '',
    ADD COLUMN universal boolean NOT NULL DEFAULT false,
    ADD COLUMN keywords  text[]  NOT NULL DEFAULT '{}',
    ADD CONSTRAINT categories_not_own_parent CHECK (parent_id <> id);

CREATE INDEX categories_parent_idx ON categories (parent_id, position);

-- +goose Down

DROP INDEX categories_parent_idx;
ALTER TABLE categories DROP CONSTRAINT categories_not_own_parent,
    DROP COLUMN keywords, DROP COLUMN universal, DROP COLUMN part_type, DROP COLUMN parent_id;
