-- +goose Up

-- A buyer's request for a catalogue part the shop does not stock. A manager
-- looks for a supplier and either quotes a price or says it was not found.
-- A quote becomes a product of its own, sold only to this buyer through the
-- usual cart, checkout and payment, and taken off sale when the quote runs
-- out unpaid.
CREATE TABLE part_requests (
    id               bigserial   PRIMARY KEY,
    user_id          bigint      NOT NULL,
    vehicle          text        NOT NULL DEFAULT '',
    brand            text        NOT NULL,
    number           text        NOT NULL,
    part_key         text        NOT NULL,
    name             text        NOT NULL,
    category         text        NOT NULL DEFAULT '',
    qty              int         NOT NULL CHECK (qty > 0),
    status           text        NOT NULL DEFAULT 'new'
                     CHECK (status IN ('new', 'quoted', 'not_found', 'declined', 'expired')),
    product_id       bigint      REFERENCES products (id),
    admin_message_id bigint,
    created_at       timestamptz NOT NULL DEFAULT now(),
    quoted_at        timestamptz,
    expires_at       timestamptz
);
CREATE INDEX part_requests_quoted_idx ON part_requests (expires_at) WHERE status = 'quoted';

-- A product made for a request is no part of the catalogue: searches and
-- menus skip it, while the buyer's cart and order see it like any other.
ALTER TABLE products ADD COLUMN request_id bigint REFERENCES part_requests (id);

-- +goose Down

ALTER TABLE products DROP COLUMN request_id;
DROP TABLE part_requests;
