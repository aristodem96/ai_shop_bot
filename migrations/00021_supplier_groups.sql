-- +goose Up

-- Where a supplier's goods go, group by group. A 1C export files every
-- product in one of the shop's groups ("Трещітки (храповики)"); the group
-- says what the product is better than words in its name do, so each group
-- is mapped to a category once -- by the model when it is sure, by the
-- owner when it is not -- and its products follow it at every import. A
-- file without groups is mapped by product name, for the names no word of
-- the tree recognised.
--
-- category_id NULL is a group not decided yet; suggestion is what the
-- model proposed for it, kept while the owner decides.
CREATE TABLE supplier_groups (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    supplier_id bigint NOT NULL REFERENCES suppliers (id) ON DELETE CASCADE,
    kind        text   NOT NULL CHECK (kind IN ('group', 'name')),
    key         text   NOT NULL,
    category_id bigint REFERENCES categories (id) ON DELETE SET NULL,
    decided_by  text   NOT NULL DEFAULT '' CHECK (decided_by IN ('', 'model', 'owner')),
    suggestion  jsonb,
    products    int    NOT NULL DEFAULT 0,
    examples    text[] NOT NULL DEFAULT '{}',
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (supplier_id, kind, key)
);
CREATE INDEX supplier_groups_open_idx ON supplier_groups (supplier_id) WHERE category_id IS NULL;

-- +goose Down

DROP TABLE supplier_groups;
