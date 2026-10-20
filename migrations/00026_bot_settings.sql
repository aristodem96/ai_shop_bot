-- +goose Up

-- What the bot remembers about itself between starts: which chat had the
-- managers' command menu, so a menu left in a chat that is no longer the
-- managers' can be taken down.
CREATE TABLE bot_settings (
    key   text PRIMARY KEY,
    value text NOT NULL
);

-- +goose Down

DROP TABLE bot_settings;
