-- +goose Up

-- What a part is, in a fixed vocabulary ("oil_filter", "spark_plug"). Names
-- are for people and vary by brand; a maintenance kit has to pick "the oil
-- filter for this car" without guessing from free text.
ALTER TABLE products ADD COLUMN part_type text NOT NULL DEFAULT '';
CREATE INDEX products_part_type_idx ON products (part_type) WHERE is_active;

-- What a service needs, per vehicle. Nullable: a vehicle added without them
-- still sells parts, it just gets no oil in its kit.
ALTER TABLE vehicles
    ADD COLUMN oil_viscosity text,
    ADD COLUMN oil_volume_l  numeric(3, 1) CHECK (oil_volume_l > 0),
    ADD COLUMN cylinders     int NOT NULL DEFAULT 4 CHECK (cylinders > 0);

-- +goose Down

ALTER TABLE vehicles DROP COLUMN cylinders, DROP COLUMN oil_volume_l, DROP COLUMN oil_viscosity;
DROP INDEX products_part_type_idx;
ALTER TABLE products DROP COLUMN part_type;
