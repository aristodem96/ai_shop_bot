-- +goose Up

-- Most buyers of a parts shop collect from the shop. A pickup order keeps
-- the shop's address, as it was when the order was placed, in delivery, so
-- the order says where the buyer was told to come even if the shop moves.
ALTER TABLE orders    ADD COLUMN pickup boolean NOT NULL DEFAULT false;
ALTER TABLE customers ADD COLUMN pickup boolean NOT NULL DEFAULT false;

-- +goose Down

ALTER TABLE customers DROP COLUMN pickup;
ALTER TABLE orders    DROP COLUMN pickup;
