-- +goose Up

-- A group whose products are of different kinds -- a brand's filters of
-- every type, "Двигун + ГРМ" -- is mapped to no one category: its products
-- are sorted one by one by the words of their names, as before groups were
-- mapped, and nobody is asked about it again.
ALTER TABLE supplier_groups ADD COLUMN mixed boolean NOT NULL DEFAULT false;

-- +goose Down

ALTER TABLE supplier_groups DROP COLUMN mixed;
