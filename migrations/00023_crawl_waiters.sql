-- +goose Up

-- Buyers waiting for their car's parts catalogue: a crawl takes minutes,
-- and a buyer who asked while it ran is told by the bot when it is done,
-- rather than left to ask again at random.
CREATE TABLE autodb_crawl_waiters (
    mod_id     bigint NOT NULL REFERENCES autodb_crawls (mod_id) ON DELETE CASCADE,
    user_id    bigint NOT NULL,
    vehicle_id bigint NOT NULL REFERENCES vehicles (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (mod_id, user_id)
);

-- +goose Down

DROP TABLE autodb_crawl_waiters;
