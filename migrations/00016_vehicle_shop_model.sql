-- +goose Up

-- An Auto-DB vehicle is named the catalogue's way, "Lacetti (J200)", while
-- price lists fit parts to the shop's own models, "Lacetti". model_id links
-- the vehicle to the shop's model it is, so the price lists' fitments reach
-- it as they reach the shop's own vehicles.
ALTER TABLE vehicles ADD COLUMN model_id bigint REFERENCES vehicle_models (id);

CREATE OR REPLACE VIEW vehicle_fitments AS
SELECT f.product_id, f.vehicle_id, f.position, f.note, true AS direct
FROM product_fitments f
UNION ALL
SELECT mf.product_id, v.id, mf.position, mf.note, false
FROM product_model_fitments mf
JOIN vehicle_models m ON m.id = mf.model_id
JOIN vehicles v ON v.make = m.make AND v.model = m.model
WHERE (mf.year_from IS NULL OR coalesce(v.year_to, 9999) >= mf.year_from)
  AND (mf.year_to IS NULL OR v.year_from <= mf.year_to)
UNION ALL
SELECT DISTINCT p.id, v.id, '', '', false
FROM vehicles v
JOIN autodb_mod_parts mp ON mp.mod_id = v.autodb_mod_id
JOIN products p ON p.part_key = mp.article_key AND p.part_key <> ''
WHERE p.brand = ''
   OR p.brand_key = mp.brand_key
   OR EXISTS (SELECT 1 FROM brand_aliases a JOIN brands b ON b.id = a.brand_id
              WHERE a.alias = mp.brand_key AND b.name = p.brand)
UNION ALL
SELECT mf.product_id, v.id, mf.position, mf.note, false
FROM product_model_fitments mf
JOIN vehicles v ON v.model_id = mf.model_id
WHERE (mf.year_from IS NULL OR coalesce(v.year_to, 9999) >= mf.year_from)
  AND (mf.year_to IS NULL OR v.year_from <= mf.year_to);

-- +goose Down

CREATE OR REPLACE VIEW vehicle_fitments AS
SELECT f.product_id, f.vehicle_id, f.position, f.note, true AS direct
FROM product_fitments f
UNION ALL
SELECT mf.product_id, v.id, mf.position, mf.note, false
FROM product_model_fitments mf
JOIN vehicle_models m ON m.id = mf.model_id
JOIN vehicles v ON v.make = m.make AND v.model = m.model
WHERE (mf.year_from IS NULL OR coalesce(v.year_to, 9999) >= mf.year_from)
  AND (mf.year_to IS NULL OR v.year_from <= mf.year_to)
UNION ALL
SELECT DISTINCT p.id, v.id, '', '', false
FROM vehicles v
JOIN autodb_mod_parts mp ON mp.mod_id = v.autodb_mod_id
JOIN products p ON p.part_key = mp.article_key AND p.part_key <> ''
WHERE p.brand = ''
   OR p.brand_key = mp.brand_key
   OR EXISTS (SELECT 1 FROM brand_aliases a JOIN brands b ON b.id = a.brand_id
              WHERE a.alias = mp.brand_key AND b.name = p.brand);
ALTER TABLE vehicles DROP COLUMN model_id;
