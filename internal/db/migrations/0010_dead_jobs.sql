-- Bounded retries (REQ-010 fix 3; standard C1 and C3).
--
-- failures counts the failures of the item itself: not blocks, not a pool
-- with no free IP. Those still delay the job (attempts and its backoff) but
-- never kill it, so a long YouTube outage can't stop collection. When
-- failures reach the job kind's limit, the job is dead: it stays in the
-- schedule with its reason, is never leased, and comes back only with
-- "ytcollect jobs requeue".
ALTER TABLE schedule ADD COLUMN failures integer NOT NULL DEFAULT 0;
ALTER TABLE schedule ADD COLUMN dead_at timestamptz;
ALTER TABLE schedule ADD COLUMN dead_reason text;
CREATE INDEX schedule_dead ON schedule (kind) WHERE dead_at IS NOT NULL;
