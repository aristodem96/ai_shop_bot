-- +goose Up

-- A vehicle is identified by what it is, not by the code someone gave it:
-- the seed and a vehicle file both say "Skoda Octavia A7 1.4 TSI from 2013",
-- and loading both must give one row, not two the buyer has to choose from.
CREATE UNIQUE INDEX vehicles_identity_key ON vehicles (make, model, engine, year_from);

-- +goose Down

DROP INDEX vehicles_identity_key;
