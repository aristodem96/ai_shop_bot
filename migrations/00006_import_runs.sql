-- +goose Up

-- One row per import that was applied (dry runs leave none): when, from
-- which file, what it changed and the report the operator saw. When a price
-- is wrong in the shop, this is where "which file set it" is answered.
CREATE TABLE import_runs (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source      text        NOT NULL,
    file_name   text        NOT NULL,
    started_at  timestamptz NOT NULL,
    finished_at timestamptz NOT NULL DEFAULT now(),
    inserted    int         NOT NULL,
    updated     int         NOT NULL,
    deactivated int         NOT NULL,
    fitments    int         NOT NULL,
    report      jsonb       NOT NULL
);

CREATE INDEX import_runs_source_idx ON import_runs (source, finished_at DESC);

-- +goose Down

DROP TABLE import_runs;
