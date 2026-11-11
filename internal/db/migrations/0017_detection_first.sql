-- The owner, D-052 (11 November): feed and tab checks find new uploads, so
-- they run at P0 with the new videos they find. At P2 they waited hours
-- behind refreshes. New rows get P0 from the registry; this moves the
-- rows already queued. Only yts's own queue changes.
UPDATE schedule SET priority = 0 WHERE kind IN ('rss', 'tabs') AND priority <> 0;
