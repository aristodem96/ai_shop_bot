-- +goose Up

-- A crawl can end empty: the catalogue has the car's tree but not one part
-- in it -- for newer modifications its data is incomplete. That is an
-- answer, not a failure to retry every few minutes; it is looked at again
-- only when the car is chosen after the month a crawl stays fresh.
ALTER TABLE autodb_crawls DROP CONSTRAINT autodb_crawls_status_check;
ALTER TABLE autodb_crawls ADD CONSTRAINT autodb_crawls_status_check
    CHECK (status IN ('queued', 'running', 'done', 'failed', 'empty'));

UPDATE autodb_crawls SET status = 'empty'
WHERE status = 'failed' AND error LIKE '%returned no parts%';

-- +goose Down

UPDATE autodb_crawls SET status = 'failed' WHERE status = 'empty';
ALTER TABLE autodb_crawls DROP CONSTRAINT autodb_crawls_status_check;
ALTER TABLE autodb_crawls ADD CONSTRAINT autodb_crawls_status_check
    CHECK (status IN ('queued', 'running', 'done', 'failed'));
