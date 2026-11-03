-- Outgoing webhooks (plan §8.2 "webhooks", D20): an outbox of events and
-- one delivery per event and endpoint, retried with backoff, then marked
-- dead (the dead-letter queue). Endpoints are configured in a file; with
-- none, events are still recorded (they are small) and nothing is sent.
CREATE TABLE webhook_events (
    id         bigserial PRIMARY KEY,
    type       text NOT NULL,          -- video.relevant, post.relevant, channel.listed, erasure
    entity_id  text NOT NULL,
    channel_id text,
    payload    jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX webhook_events_recent ON webhook_events (created_at);
CREATE INDEX webhook_events_entity ON webhook_events (entity_id);
CREATE INDEX webhook_events_channel ON webhook_events (channel_id) WHERE channel_id IS NOT NULL;

-- An endpoint gets the events after first_event (none from before it was
-- added).
CREATE TABLE webhook_endpoints (
    name        text PRIMARY KEY,
    first_event bigint NOT NULL,
    added_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE webhook_deliveries (
    event_id     bigint NOT NULL REFERENCES webhook_events (id) ON DELETE CASCADE,
    endpoint     text NOT NULL,
    status       text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'delivered', 'dead')),
    attempts     integer NOT NULL DEFAULT 0,
    next_at      timestamptz NOT NULL DEFAULT now(),
    lease_until  timestamptz,
    last_status  integer,                -- HTTP status of the last attempt
    last_error   text,
    delivered_at timestamptz,
    PRIMARY KEY (event_id, endpoint)
);
CREATE INDEX webhook_deliveries_due ON webhook_deliveries (next_at) WHERE status = 'pending';

-- Events are recorded by triggers, so every path that marks something
-- relevant or lists a channel is covered.
CREATE FUNCTION webhook_video_relevant() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.relevant AND (TG_OP = 'INSERT' OR NOT COALESCE(OLD.relevant, false)) THEN
        INSERT INTO webhook_events (type, entity_id, channel_id, payload)
        SELECT 'video.relevant', NEW.id, NEW.channel_id, jsonb_build_object(
            'video_id', NEW.id, 'channel_id', NEW.channel_id, 'channel_title', c.title, 'channel_kind', c.kind,
            'kind', NEW.kind, 'title', NEW.title, 'published_at', NEW.published_at,
            'matched_keywords', NEW.matched_keywords, 'url', 'https://www.youtube.com/watch?v=' || NEW.id)
        FROM (SELECT 1) one LEFT JOIN channels c ON c.id = NEW.channel_id;
    END IF;
    RETURN NULL;
END $$;
CREATE TRIGGER videos_webhook AFTER INSERT OR UPDATE OF relevant ON videos
    FOR EACH ROW EXECUTE FUNCTION webhook_video_relevant();

CREATE FUNCTION webhook_post_relevant() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.relevant AND (TG_OP = 'INSERT' OR NOT COALESCE(OLD.relevant, false)) THEN
        INSERT INTO webhook_events (type, entity_id, channel_id, payload)
        SELECT 'post.relevant', NEW.id, NEW.channel_id, jsonb_build_object(
            'post_id', NEW.id, 'channel_id', NEW.channel_id, 'channel_title', c.title, 'channel_kind', c.kind,
            'text', left(NEW.text, 2000), 'published_at', NEW.published_at,
            'matched_keywords', NEW.matched_keywords, 'url', 'https://www.youtube.com/post/' || NEW.id)
        FROM (SELECT 1) one LEFT JOIN channels c ON c.id = NEW.channel_id;
    END IF;
    RETURN NULL;
END $$;
CREATE TRIGGER posts_webhook AFTER INSERT OR UPDATE OF relevant ON posts
    FOR EACH ROW EXECUTE FUNCTION webhook_post_relevant();

CREATE FUNCTION webhook_channel_listed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.status = 'listed' AND (TG_OP = 'INSERT' OR OLD.status IS DISTINCT FROM 'listed') THEN
        INSERT INTO webhook_events (type, entity_id, channel_id, payload)
        VALUES ('channel.listed', NEW.id, NEW.id, jsonb_build_object(
            'channel_id', NEW.id, 'kind', NEW.kind, 'tier', NEW.tier, 'handle', NEW.handle, 'title', NEW.title,
            'added_by', NEW.added_by, 'url', 'https://www.youtube.com/channel/' || NEW.id));
    END IF;
    RETURN NULL;
END $$;
CREATE TRIGGER channels_webhook AFTER INSERT OR UPDATE OF status ON channels
    FOR EACH ROW EXECUTE FUNCTION webhook_channel_listed();
