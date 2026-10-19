-- +goose Up

-- The cars an article fits, as getArtInfo lists them: at most 100, the API
-- cutting the list there. NULL for an article fetched before they were
-- kept, which is fetched again when they are wanted.
ALTER TABLE autodb_articles ADD COLUMN vehicles jsonb;

-- +goose Down

ALTER TABLE autodb_articles DROP COLUMN vehicles;
