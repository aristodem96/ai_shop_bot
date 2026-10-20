-- +goose Up

-- The shop's category of a catalogue section, from the category words in
-- its path: "тормозная система › Тормозной диск" is brake discs. A part is
-- filed by it until its translated name says better, so a car's catalogue
-- fills the menu as soon as it is loaded rather than when the model has
-- translated thousands of names.
ALTER TABLE autodb_sections ADD COLUMN category text NOT NULL DEFAULT '';

-- +goose Down

ALTER TABLE autodb_sections DROP COLUMN category;
