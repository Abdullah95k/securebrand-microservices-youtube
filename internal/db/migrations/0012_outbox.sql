-- REQ-010 fix 12, PR 1: the transactional outbox. Additions only.
--
-- Every write that changes what yts knows adds an event row here in the
-- same transaction as the data, so an event exists exactly when its data
-- does. The relay (a later PR) publishes the rows to Kafka in id order and
-- sets published_at. Sent rows are deleted after 7 days (the owner, 7
-- November): they are copies made for sending. Unsent rows are never
-- deleted.
CREATE TABLE event_outbox (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- channel, channel_snapshot, video, video_metrics, post, post_metrics,
    -- comment, comment_metrics, playlist, author (internal/outbox).
    kind          text NOT NULL,
    -- The YouTube ID of the thing the event is about.
    entity_id     text NOT NULL,
    -- The YouTube ID the Kafka key is built from: the post (video or
    -- community post) for comments and counts, so they stay in order;
    -- otherwise the entity itself.
    partition_key text NOT NULL,
    -- When yts read it. The contract's version is this, in microseconds.
    observed_at   timestamptz NOT NULL,
    -- The stored record after this write (content events), or the counts
    -- (metrics events).
    payload       jsonb NOT NULL,
    -- The job, the operations and raw responses read, the exit country,
    -- the collector's and parser's versions.
    provenance    jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at    timestamptz NOT NULL DEFAULT now(),
    -- Set by the relay.
    attempts      integer NOT NULL DEFAULT 0,
    published_at  timestamptz,
    failure       text
);

CREATE INDEX event_outbox_unsent ON event_outbox (id) WHERE published_at IS NULL;
CREATE INDEX event_outbox_sent ON event_outbox (published_at) WHERE published_at IS NOT NULL;

-- What the outbox last emitted per entity, so an event is written only
-- when something changed: the hash of the content (counts and bookkeeping
-- left out), and the hash and time of the last counts. Counts go out when
-- they change, and at least daily for every item still read (the
-- contract's volume rule).
CREATE TABLE outbox_state (
    kind         text NOT NULL,
    entity_id    text NOT NULL,
    content_hash bytea,
    metrics_hash bytea,
    metrics_at   timestamptz,
    PRIMARY KEY (kind, entity_id)
);
