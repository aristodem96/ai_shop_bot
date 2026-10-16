-- +goose Up

-- Keywords and categories the shop's owner adds in the managers' chat. They
-- live apart from the ones data/categories.csv brings, which are synced
-- over on every start: an owner's word stored with those would be erased by
-- the next deploy. The classifier reads both.
--
-- A word is unique across the tree: the same fragment leading to two
-- categories would make filing depend on their order.
CREATE TABLE category_keywords (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    category_id bigint NOT NULL REFERENCES categories (id) ON DELETE CASCADE,
    word        text   NOT NULL UNIQUE CHECK (char_length(word) BETWEEN 3 AND 40),
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- A category the owner created rather than the file: the sync leaves it be.
ALTER TABLE categories ADD COLUMN custom boolean NOT NULL DEFAULT false;

-- +goose Down

ALTER TABLE categories DROP COLUMN custom;
DROP TABLE category_keywords;
