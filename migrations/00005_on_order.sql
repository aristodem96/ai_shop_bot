-- +goose Up

-- A part the shop does not keep but its supplier delivers: most of a price
-- list is that. NULL means the shop sells only what is on its shelf, as
-- before. A number means "on order, delivered in about this many days": such
-- a part is sold in any quantity and its stock is not tracked -- the supplier's
-- warehouse is, and the shop does not see it.
ALTER TABLE products ADD COLUMN lead_time_days int CHECK (lead_time_days >= 0);

-- Whether the line took units from the shelf when the order was placed. It
-- is recorded rather than looked up later because a re-import may switch a
-- product between the two modes while the order is open, and giving back
-- units that were never taken would create stock out of nothing.
ALTER TABLE order_items ADD COLUMN from_stock boolean NOT NULL DEFAULT true;

-- +goose Down

ALTER TABLE order_items DROP COLUMN from_stock;
ALTER TABLE products DROP COLUMN lead_time_days;
