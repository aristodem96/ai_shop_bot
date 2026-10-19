-- +goose Up

-- A cache of the Auto-DB Pro vehicle directory: makes, the models of a make,
-- the modifications (engines) of a model. The API takes seconds per call,
-- so nothing a buyer waits on reads it directly; each list is fetched once
-- and kept, and the *_fetched tables record that a list was fetched even
-- when it came back empty.
CREATE TABLE autodb_makes (
    id         bigint PRIMARY KEY,
    name       text NOT NULL,
    fetched_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE autodb_models (
    id         bigint PRIMARY KEY,
    make_id    bigint NOT NULL REFERENCES autodb_makes (id) ON DELETE CASCADE,
    name       text NOT NULL,
    years      text NOT NULL DEFAULT ''
);
CREATE INDEX autodb_models_make_idx ON autodb_models (make_id);

CREATE TABLE autodb_models_fetched (
    make_id    bigint PRIMARY KEY REFERENCES autodb_makes (id) ON DELETE CASCADE,
    fetched_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE autodb_modifications (
    id       bigint PRIMARY KEY,
    model_id bigint NOT NULL REFERENCES autodb_models (id) ON DELETE CASCADE,
    name     text   NOT NULL,
    attrs    jsonb  NOT NULL DEFAULT '{}'
);
CREATE INDEX autodb_modifications_model_idx ON autodb_modifications (model_id);

CREATE TABLE autodb_modifications_fetched (
    model_id   bigint PRIMARY KEY REFERENCES autodb_models (id) ON DELETE CASCADE,
    fetched_at timestamptz NOT NULL DEFAULT now()
);

-- A vehicle a buyer chose from the Auto-DB directory is a row of vehicles
-- like any other, so garages, labels and kits keep working; this is the
-- modification it is, which is what fitments are asked by.
ALTER TABLE vehicles ADD COLUMN autodb_mod_id bigint UNIQUE;

-- +goose Down

ALTER TABLE vehicles DROP COLUMN autodb_mod_id;
DROP TABLE autodb_modifications_fetched;
DROP TABLE autodb_modifications;
DROP TABLE autodb_models_fetched;
DROP TABLE autodb_models;
DROP TABLE autodb_makes;
