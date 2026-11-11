-- The owner, D-052 (11 November): a job's priority follows what it does
-- each time it runs. Until now a job kept the priority it was first queued
-- with, so every later reading of a video found as new stayed at P0, and
-- its comment checks at P1, for as long as yts followed it, ahead of every
-- refresh. The workers set the priority after each run from now on; this
-- moves the rows already queued, by the same rule. Only yts's own queue
-- changes.

-- A video read at least once: its later readings are refreshes (P2), except
-- a stream that is live or about to start.
UPDATE schedule s SET priority = 2 FROM videos v
    WHERE s.kind = 'video' AND s.entity_id = v.id AND s.priority IN (0, 1, 3)
      AND v.last_checked IS NOT NULL AND COALESCE(v.live_status, 'none') NOT IN ('live', 'upcoming');

-- Comment checks: new comments (P1) while the video or post is under two
-- days old, refreshes (P2) after; a backfilled one's (P3) once it has run.
UPDATE schedule s SET priority = 2 FROM videos v
    WHERE s.kind = 'comments' AND s.entity_id = v.id
      AND (s.priority = 1 AND v.published_at < now() - interval '2 days' OR s.priority = 3 AND s.last_ok IS NOT NULL);
UPDATE schedule s SET priority = 2 FROM posts p
    WHERE s.kind = 'post_comments' AND s.entity_id = p.id
      AND (s.priority = 1 AND p.published_at < now() - interval '2 days' OR s.priority = 3 AND s.last_ok IS NOT NULL);
