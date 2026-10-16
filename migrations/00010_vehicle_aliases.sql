-- +goose Up

-- How a supplier names a vehicle, mapped to the catalogue's models: "Chery
-- Tiggo" to Chery Tiggo, "Chevrolet Aveo" to the Aveo T200 and T250. The
-- owner confirms each in the managers' chat, a model suggesting; a profile's
-- "vehicles" map does the same in a file, for a developer. Per supplier: the
-- same words may mean different cars in two price lists.
CREATE TABLE vehicle_aliases (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source     text   NOT NULL,
    -- As the supplier writes it, for people; name_key is what matching
    -- compares, letters and digits in lower case.
    name       text   NOT NULL,
    name_key   text   NOT NULL,
    model_id   bigint NOT NULL REFERENCES vehicle_models (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source, name_key, model_id)
);

-- Which of the file's vehicle names a model fitment came from, as a
-- name_key. Removing a mapping then removes its fitments at once, instead
-- of leaving parts "fitting" a car until the supplier's next price list.
ALTER TABLE product_model_fitments ADD COLUMN via text NOT NULL DEFAULT '';

-- +goose Down

ALTER TABLE product_model_fitments DROP COLUMN via;
DROP TABLE vehicle_aliases;
