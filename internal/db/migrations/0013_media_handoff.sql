-- REQ-010 fix 12, PR 7: the media hand-off to analysis. Additions only.
--
-- The relay announces each stored image of the handed-off kinds
-- (thumbnails; community-post images when they are collected) on
-- content.media.ready.v1 with a deadline, reads the acknowledgements on
-- content.media.processed.v1, and deletes the file once every required
-- consumer has acknowledged it, or at its deadline, saying so on
-- content.media.deleted.v1. The media row (the record) stays forever.
-- Without a relay (no Kafka: the pilot), nothing is announced or deleted.
ALTER TABLE media ADD COLUMN announced_at  timestamptz; -- first announced
ALTER TABLE media ADD COLUMN delete_after  timestamptz; -- the file's deadline
ALTER TABLE media ADD COLUMN attempt       integer NOT NULL DEFAULT 0; -- announcements: 1, and up to 3 more
ALTER TABLE media ADD COLUMN deleted_at    timestamptz; -- the file was removed from storage
ALTER TABLE media ADD COLUMN delete_reason text;        -- processed or expired

CREATE INDEX media_handoff ON media (delete_after) WHERE announced_at IS NOT NULL AND deleted_at IS NULL;

-- Each consumer's latest acknowledgement of a file.
CREATE TABLE media_acks (
    kind         text NOT NULL,
    entity_id    text NOT NULL,
    sha256       text NOT NULL,
    consumer     text NOT NULL,
    -- processed, failed_permanent, failed_retryable, skipped
    status       text NOT NULL,
    reason       text,
    error        text,
    processed_at timestamptz,
    received_at  timestamptz NOT NULL,
    PRIMARY KEY (kind, entity_id, sha256, consumer)
);
