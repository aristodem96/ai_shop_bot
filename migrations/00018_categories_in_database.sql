-- +goose Up

-- The category tree is the shop's, kept in the database and edited in the
-- managers' chat. data/categories.csv only fills an empty database; it no
-- longer overwrites the tree on every start. So the file's words and the
-- owner's are one list, in category_keywords.
INSERT INTO category_keywords (category_id, word)
SELECT c.id, w FROM categories c, unnest(c.keywords) AS w
WHERE char_length(w) BETWEEN 3 AND 40
ON CONFLICT (word) DO NOTHING;
ALTER TABLE categories DROP COLUMN keywords, DROP COLUMN custom;

-- The supplier's own group of a product ("Гальмівна система" in a 1C
-- export), kept to sort it again when the tree changes, as an import would.
ALTER TABLE products ADD COLUMN source_group text NOT NULL DEFAULT '';

-- +goose Down

ALTER TABLE products DROP COLUMN source_group;
ALTER TABLE categories ADD COLUMN keywords text[] NOT NULL DEFAULT '{}',
    ADD COLUMN custom boolean NOT NULL DEFAULT false;
