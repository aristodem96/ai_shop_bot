-- +goose Up

-- Cars whose catalogue came back empty -- the API answering an overloaded
-- crawl with empty lists -- were called done and would have sat empty for a
-- month. Sections marked stored with no parts in them are asked again, and
-- such cars are queued anew.
DELETE FROM autodb_mod_sections s
WHERE NOT EXISTS (SELECT 1 FROM autodb_mod_parts p WHERE p.mod_id = s.mod_id AND p.section_id = s.section_id);

UPDATE autodb_crawls c SET status = 'queued', queued_at = now(), error = ''
WHERE c.status = 'done' AND NOT EXISTS (SELECT 1 FROM autodb_mod_parts p WHERE p.mod_id = c.mod_id);

-- +goose Down

-- Nothing to undo: the queued crawls simply run.
SELECT 1;
