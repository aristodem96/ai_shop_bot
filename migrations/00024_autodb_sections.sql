-- +goose Up

-- The catalogue's sections that list parts, with their paths in the tree
-- ("тормозная система › Тормозные колодки"). A section's id is the same for
-- every vehicle, so a path learnt from one car's tree finds the section for
-- the next: a buyer's search for pads loads that section of their car at
-- once, without waiting for the whole tree.
CREATE TABLE autodb_sections (
    id   bigint PRIMARY KEY,
    path text   NOT NULL
);

-- Which sections of a modification are stored, and when: a crawl skips what
-- a buyer's search has just loaded, and a search what the crawl has.
CREATE TABLE autodb_mod_sections (
    mod_id     bigint      NOT NULL,
    section_id bigint      NOT NULL,
    fetched_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (mod_id, section_id)
);

-- What is crawled already counts as stored.
INSERT INTO autodb_mod_sections (mod_id, section_id, fetched_at)
SELECT DISTINCT mp.mod_id, mp.section_id, coalesce(c.finished_at, now())
FROM autodb_mod_parts mp LEFT JOIN autodb_crawls c ON c.mod_id = mp.mod_id;

-- The catalogue calls a ball joint "Несущий / направляющий шарнир", a word
-- the shop's tree lacked; plain "шарнир" would take CV joints too.
INSERT INTO category_keywords (category_id, word)
SELECT id, 'направляющий шарнир' FROM categories WHERE slug = 'arms'
ON CONFLICT (word) DO NOTHING;

-- +goose Down

DELETE FROM category_keywords WHERE word = 'направляющий шарнир';
DROP TABLE autodb_mod_sections;
DROP TABLE autodb_sections;
