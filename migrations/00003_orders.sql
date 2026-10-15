-- +goose Up

-- Contacts are remembered per buyer, so a second order is two taps.
CREATE TABLE customers (
    user_id    bigint PRIMARY KEY,
    name       text NOT NULL,
    phone      text NOT NULL,
    delivery   text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE orders (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id        bigint NOT NULL,
    status         text   NOT NULL CHECK (status IN (
        'awaiting_payment', 'paid', 'confirmed', 'shipped', 'cancelled', 'expired', 'needs_refund')),
    -- In kopiyky, the sum of the frozen line prices: exactly what the
    -- invoice asks for.
    total          bigint NOT NULL CHECK (total > 0),
    customer_name  text   NOT NULL,
    phone          text   NOT NULL,
    delivery       text   NOT NULL,
    invoice_id     text UNIQUE,
    payment_url    text,
    -- The newest payment event applied. Monobank does not deliver webhooks
    -- in order -- "success" may arrive before "processing" -- so an event
    -- older than this one is stale and ignored.
    payment_modified_at timestamptz,
    reserved_until timestamptz NOT NULL,
    paid_at        timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX orders_user_idx ON orders (user_id, created_at DESC);
-- The expiry job scans only open orders.
CREATE INDEX orders_reservation_idx ON orders (reserved_until) WHERE status = 'awaiting_payment';
-- One unpaid order per buyer: a second tap on "pay" finds the first order
-- instead of reserving the stock twice.
CREATE UNIQUE INDEX orders_one_open_per_user ON orders (user_id) WHERE status = 'awaiting_payment';

CREATE TABLE order_items (
    order_id   bigint NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    product_id bigint NOT NULL REFERENCES products (id),
    -- Name and price are copied, not joined: the order records what was
    -- bought and for how much, whatever the catalogue says later.
    name       text   NOT NULL,
    brand      text   NOT NULL,
    price      bigint NOT NULL CHECK (price > 0),
    qty        int    NOT NULL CHECK (qty > 0),
    PRIMARY KEY (order_id, product_id)
);

-- +goose Down

DROP TABLE order_items;
DROP TABLE orders;
DROP TABLE customers;
