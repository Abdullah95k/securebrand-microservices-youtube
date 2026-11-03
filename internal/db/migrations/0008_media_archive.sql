-- Media (plan §8.2 "media") and the compressed archive (§8.2 "archiver").
--
-- media: one row per distinct image of a video thumbnail, a channel's avatar
-- or banner, or an enriched commenter's avatar. Images are stored by content
-- (sha256), so an unchanged picture is kept once and every change is kept
-- (D26: nothing is deleted except on erasure).
CREATE TABLE media (
    kind         text NOT NULL CHECK (kind IN ('thumb', 'avatar', 'banner', 'author_avatar')),
    entity_id    text NOT NULL,         -- video, channel or author ID
    channel_id   text,                  -- the channel it belongs to (erasure by channel)
    sha256       text NOT NULL,
    object_key   text NOT NULL,
    source_url   text NOT NULL,
    content_type text NOT NULL,
    bytes        integer NOT NULL,
    first_seen   timestamptz NOT NULL,
    last_seen    timestamptz NOT NULL,
    PRIMARY KEY (kind, entity_id, sha256)
);
CREATE INDEX media_channel ON media (channel_id) WHERE channel_id IS NOT NULL;

-- archive_index: the parts (gzip JSON lines in object storage) of each
-- archived monthly partition. scrubbed_at: the suppression list has been
-- applied to the part up to this time (erasures after it are scrubbed by the
-- next run).
CREATE TABLE archive_index (
    table_name  text NOT NULL,
    month       date NOT NULL,
    part        integer NOT NULL,
    object_key  text NOT NULL,
    rows        bigint NOT NULL,
    bytes       bigint NOT NULL,
    sha256      text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    scrubbed_at timestamptz NOT NULL,
    PRIMARY KEY (table_name, month, part)
);

-- archive_months: a month whose partition was archived, checked and dropped
-- from Postgres. restored_until: an admin restored it into Postgres; the
-- archiver leaves it there until then.
CREATE TABLE archive_months (
    table_name     text NOT NULL,
    month          date NOT NULL,
    rows           bigint NOT NULL,
    parts          integer NOT NULL,
    archived_at    timestamptz NOT NULL DEFAULT now(),
    restored_until timestamptz,
    PRIMARY KEY (table_name, month)
);
