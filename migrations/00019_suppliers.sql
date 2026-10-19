-- +goose Up

-- Where price lists come from, and how each one's files are read. Made in
-- the managers' chat from a supplier's first file; source is the key its
-- products are matched on, made from the name once and never changed.
CREATE TABLE suppliers (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name           text    NOT NULL UNIQUE,
    source         text    NOT NULL UNIQUE,
    columns        jsonb   NOT NULL,
    lead_days      int     CHECK (lead_days >= 0),
    markup_percent numeric NOT NULL DEFAULT 0 CHECK (markup_percent BETWEEN 0 AND 500),
    created_at     timestamptz NOT NULL DEFAULT now()
);

-- +goose Down

DROP TABLE suppliers;
