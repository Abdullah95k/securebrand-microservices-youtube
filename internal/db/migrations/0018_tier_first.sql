-- The owner, D-052 (11 November): within a priority, tier A's jobs go
-- first, then tier B's, then everyone else's (tiers C, D and M, and jobs of
-- no listed channel), oldest due first, as before. tier_rank holds that
-- order on each queued row, and triggers keep it in step with the
-- channel's tier. Additions only, on yts's own queue.
ALTER TABLE schedule ADD COLUMN tier_rank smallint NOT NULL DEFAULT 2;

CREATE FUNCTION queue_tier_rank(tier text) RETURNS smallint LANGUAGE sql IMMUTABLE AS $$
    SELECT (CASE tier WHEN 'A' THEN 0 WHEN 'B' THEN 1 ELSE 2 END)::smallint
$$;

-- A queued row takes its channel's rank.
CREATE FUNCTION schedule_tier_rank() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.tier_rank := COALESCE((SELECT queue_tier_rank(tier) FROM channels WHERE id = NEW.channel_id), 2);
    RETURN NEW;
END $$;
CREATE TRIGGER schedule_tier_rank BEFORE INSERT OR UPDATE OF channel_id ON schedule
    FOR EACH ROW EXECUTE FUNCTION schedule_tier_rank();

-- A channel added, or moved to another tier, takes its queued rows along.
CREATE FUNCTION channels_tier_rank() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE schedule SET tier_rank = queue_tier_rank(NEW.tier)
        WHERE channel_id = NEW.id AND tier_rank <> queue_tier_rank(NEW.tier);
    RETURN NULL;
END $$;
CREATE TRIGGER channels_tier_rank AFTER INSERT OR UPDATE OF tier ON channels
    FOR EACH ROW EXECUTE FUNCTION channels_tier_rank();

-- The rows already queued.
UPDATE schedule s SET tier_rank = queue_tier_rank(c.tier)
    FROM channels c WHERE c.id = s.channel_id AND c.tier IN ('A', 'B');

-- The lease reads due jobs in this order.
DROP INDEX schedule_due;
CREATE INDEX schedule_due ON schedule (priority, tier_rank, due_at);
