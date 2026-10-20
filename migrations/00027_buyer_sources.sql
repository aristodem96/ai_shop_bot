-- +goose Up

-- Where each buyer first came from: the mark of the link that opened the
-- bot (t.me/bot?start=sto_ivan), '' with none. Only the first is kept, so a
-- buyer a mechanic sent stays the mechanic's however they come back.
CREATE TABLE buyer_sources (
    user_id    bigint PRIMARY KEY,
    source     text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX buyer_sources_created_idx ON buyer_sources (created_at);

-- +goose Down

DROP TABLE buyer_sources;
