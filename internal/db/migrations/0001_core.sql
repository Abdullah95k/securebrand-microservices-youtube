-- SecureBrand YouTube collector: core schema (plan §8.3).
--
-- Everything is kept forever (plan D18, D26). Rows are removed only by an
-- erasure request (§8.9) or, for official-API search hits, after 30 days
-- (YouTube's API rule). Large time series are partitioned by month so the
-- archiver can move whole old partitions cheaply; partitions are created by
-- the application (db.EnsurePartitions), with a default partition as a
-- safety net.
--
-- Accuracy: counts the website shows abbreviated ("12K") are stored as a
-- lower bound (_min) plus the text; estimated times carry their precision
-- in seconds. Every content row records its source.

-- ---------------------------------------------------------------- channels

CREATE TABLE channels (
    id               text PRIMARY KEY,               -- UC…
    handle           text,
    title            text,
    description      text,
    keywords         text,                            -- the channel's own keyword field
    country          text,
    joined_at        date,
    links            jsonb NOT NULL DEFAULT '[]',
    subscribers_min  bigint,
    subscribers_text text,
    videos_min       bigint,
    views_min        bigint,
    avatar_url       text,
    banner_url       text,
    kind             text NOT NULL DEFAULT 'core' CHECK (kind IN ('core', 'mention')),
    tier             text NOT NULL DEFAULT 'C' CHECK (tier IN ('A', 'B', 'C', 'D', 'M')),
    status           text NOT NULL DEFAULT 'listed'
                     CHECK (status IN ('candidate', 'listed', 'dormant', 'no_match', 'removed')),
    added_by         text NOT NULL DEFAULT 'admin' CHECK (added_by IN ('seed', 'admin', 'discovery', 'enrichment')),
    added_reason     text,
    backfill_until   timestamptz,                     -- uploads back to here have been read
    backfill_done_at timestamptz,
    first_seen       timestamptz NOT NULL DEFAULT now(),
    last_checked     timestamptz,
    removed_at       timestamptz,
    source           text NOT NULL DEFAULT 'web'
);
CREATE INDEX channels_status_tier ON channels (status, tier);
CREATE UNIQUE INDEX channels_handle ON channels (lower(handle)) WHERE handle IS NOT NULL;

CREATE TABLE channel_snapshots (
    channel_id       text NOT NULL,
    taken_at         timestamptz NOT NULL,
    subscribers_min  bigint,
    subscribers_text text,
    videos_min       bigint,
    views_min        bigint,
    PRIMARY KEY (channel_id, taken_at)
) PARTITION BY RANGE (taken_at);

CREATE TABLE channel_changes (
    channel_id text NOT NULL,
    changed_at timestamptz NOT NULL,
    field      text NOT NULL,
    old_value  text,
    new_value  text
);
CREATE INDEX channel_changes_channel ON channel_changes (channel_id, changed_at);

-- ------------------------------------------------------------------ videos

CREATE TABLE videos (
    id                    text PRIMARY KEY,
    channel_id            text NOT NULL,
    kind                  text NOT NULL CHECK (kind IN ('video', 'short', 'live')),
    title                 text,
    description           text,
    published_at          timestamptz,
    published_precision_s integer,
    duration_s            integer,
    views                 bigint,
    likes_min             bigint,
    likes_text            text,
    comments_min          bigint,
    comments_text         text,
    comments_disabled     boolean,
    tags                  text[],
    category              text,
    player_unavailable    boolean NOT NULL DEFAULT false,  -- tags and category could not be read
    live_status           text CHECK (live_status IN ('none', 'upcoming', 'live', 'was_live')),
    scheduled_start       timestamptz,
    relevant              boolean NOT NULL DEFAULT false,
    matched_keywords      text[] NOT NULL DEFAULT '{}',
    comments_partial      boolean NOT NULL DEFAULT false,  -- backfill hit the cap
    comments_complete_pct real,                            -- collected ÷ displayed
    first_seen            timestamptz NOT NULL DEFAULT now(),
    last_checked          timestamptz,
    removed_at            timestamptz,
    missed_checks         smallint NOT NULL DEFAULT 0,
    source                text NOT NULL DEFAULT 'web'
);
CREATE INDEX videos_channel_published ON videos (channel_id, published_at DESC);
CREATE INDEX videos_relevant ON videos (published_at DESC) WHERE relevant;

CREATE TABLE video_snapshots (
    video_id     text NOT NULL,
    taken_at     timestamptz NOT NULL,
    views        bigint,
    likes_min    bigint,
    comments_min bigint,
    live_viewers bigint,
    PRIMARY KEY (video_id, taken_at)
) PARTITION BY RANGE (taken_at);

CREATE TABLE video_changes (
    video_id   text NOT NULL,
    changed_at timestamptz NOT NULL,
    field      text NOT NULL,
    old_value  text,
    new_value  text
);
CREATE INDEX video_changes_video ON video_changes (video_id, changed_at);

-- ----------------------------------------------------- authors (commenters)

CREATE TABLE authors (
    id            text PRIMARY KEY,                  -- the commenter's channel ID
    handle        text,
    display_name  text,
    avatar_url    text,
    first_seen    timestamptz NOT NULL DEFAULT now(),
    last_seen     timestamptz NOT NULL DEFAULT now(),
    comments_seen bigint NOT NULL DEFAULT 0,
    relevant_comments bigint NOT NULL DEFAULT 0,      -- on Iraq-relevant items: enrichment order
    enriched_at   timestamptz
);

CREATE TABLE author_profiles (
    author_id       text NOT NULL,
    fetched_at      timestamptz NOT NULL,
    handle          text,
    display_name    text,
    description     text,
    country         text,
    joined_at       date,
    links           jsonb,
    subscribers_min bigint,
    videos_min      bigint,
    avatar_url      text,
    banner_url      text,
    PRIMARY KEY (author_id, fetched_at)
) PARTITION BY RANGE (fetched_at);

CREATE TABLE author_badges (
    author_id  text NOT NULL,
    badge      text NOT NULL,                        -- verified, member, …
    detail     text,                                 -- e.g. "member for 2 years"
    channel_id text NOT NULL DEFAULT '',             -- membership is per channel
    first_seen timestamptz NOT NULL DEFAULT now(),
    last_seen  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (author_id, badge, channel_id)
);

-- ------------------------------------------------------- comments, replies

-- One row per comment with its current state; every text version we saw is
-- in comment_versions. Comments on videos and on community posts share the
-- table (parent_kind).
CREATE TABLE comments (
    id                    text PRIMARY KEY,
    parent_kind           text NOT NULL CHECK (parent_kind IN ('video', 'post')),
    parent_id             text NOT NULL,
    channel_id            text NOT NULL,             -- owner of the video or post
    thread_id             text,                      -- top-level comment of a reply; NULL for top level
    author_id             text,
    author_handle         text,
    author_name           text,
    author_avatar_url     text,
    author_is_creator     boolean NOT NULL DEFAULT false,
    author_badges         jsonb,
    text                  text NOT NULL,
    text_hash             bytea NOT NULL,
    published_at          timestamptz,
    published_precision_s integer,
    likes_min             bigint,
    likes_text            text,
    reply_count           integer,
    pinned                boolean NOT NULL DEFAULT false,
    hearted               boolean NOT NULL DEFAULT false,
    edited                boolean NOT NULL DEFAULT false,
    version               integer NOT NULL DEFAULT 1,
    first_seen            timestamptz NOT NULL,
    last_seen             timestamptz NOT NULL,
    missed_sweeps         smallint NOT NULL DEFAULT 0,  -- 2 misses → deleted
    deleted_at            timestamptz,
    source                text NOT NULL DEFAULT 'web'
);
CREATE INDEX comments_parent ON comments (parent_id, first_seen);
CREATE INDEX comments_thread ON comments (thread_id) WHERE thread_id IS NOT NULL;
CREATE INDEX comments_author ON comments (author_id);
CREATE INDEX comments_channel ON comments (channel_id);

CREATE TABLE comment_versions (
    comment_id text NOT NULL,
    version    integer NOT NULL,
    seen_at    timestamptz NOT NULL,
    text       text NOT NULL,
    edited     boolean NOT NULL DEFAULT false,
    PRIMARY KEY (comment_id, version, seen_at)
) PARTITION BY RANGE (seen_at);

-- ----------------------------------------------------------- posts, lists

CREATE TABLE posts (
    id                    text PRIMARY KEY,
    channel_id            text NOT NULL,
    text                  text,
    attachments           jsonb,                     -- images, polls, linked videos
    published_at          timestamptz,
    published_precision_s integer,
    likes_min             bigint,
    likes_text            text,
    comments_min          bigint,
    comments_text         text,
    relevant              boolean NOT NULL DEFAULT false,
    matched_keywords      text[] NOT NULL DEFAULT '{}',
    first_seen            timestamptz NOT NULL DEFAULT now(),
    last_checked          timestamptz,
    removed_at            timestamptz,
    source                text NOT NULL DEFAULT 'web'
);
CREATE INDEX posts_channel_published ON posts (channel_id, published_at DESC);

CREATE TABLE post_snapshots (
    post_id      text NOT NULL,
    taken_at     timestamptz NOT NULL,
    likes_min    bigint,
    comments_min bigint,
    PRIMARY KEY (post_id, taken_at)
) PARTITION BY RANGE (taken_at);

CREATE TABLE playlists (
    id           text PRIMARY KEY,
    channel_id   text NOT NULL,
    title        text,
    description  text,
    video_count  integer,
    relevant     boolean NOT NULL DEFAULT false,
    first_seen   timestamptz NOT NULL DEFAULT now(),
    last_checked timestamptz,
    removed_at   timestamptz
);
CREATE INDEX playlists_channel ON playlists (channel_id);

CREATE TABLE playlist_items (
    playlist_id text NOT NULL,
    video_id    text NOT NULL,
    position    integer,
    first_seen  timestamptz NOT NULL DEFAULT now(),
    last_seen   timestamptz NOT NULL DEFAULT now(),
    removed_at  timestamptz,
    PRIMARY KEY (playlist_id, video_id)
);

-- --------------------------------------------------------------- discovery

CREATE TABLE languages (
    code    text PRIMARY KEY,                        -- ar, ckb, kmr, en, tr, fa, …
    name    text NOT NULL,
    enabled boolean NOT NULL DEFAULT true
);

-- Your own brand and person keywords live here, not in the repo (§8.10).
CREATE TABLE keywords (
    id         bigserial PRIMARY KEY,
    label      text NOT NULL,
    kind       text NOT NULL CHECK (kind IN ('place', 'person', 'org', 'topic', 'brand', 'term')),
    ambiguous  boolean NOT NULL DEFAULT false,       -- needs a second keyword to count
    active     boolean NOT NULL DEFAULT true,
    searched   boolean NOT NULL DEFAULT true,        -- used as a search term, not only for matching
    source     text NOT NULL DEFAULT 'admin' CHECK (source IN ('seed', 'admin', 'suggestion')),
    seed_id    text UNIQUE,                          -- the seed list's ID, for re-imports
    note       text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE keyword_variants (
    keyword_id bigint NOT NULL REFERENCES keywords (id) ON DELETE CASCADE,
    variant    text NOT NULL,
    normalized text NOT NULL,
    lang       text,
    PRIMARY KEY (keyword_id, normalized)
);
CREATE INDEX keyword_variants_normalized ON keyword_variants (normalized);

CREATE TABLE keyword_suggestions (
    id          bigserial PRIMARY KEY,
    term        text NOT NULL,
    normalized  text NOT NULL UNIQUE,
    score       real NOT NULL,
    evidence    jsonb,
    status      text NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'accepted', 'rejected')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    decided_at  timestamptz
);

-- API hits are deleted after 30 days (YouTube's API rule); website hits are kept.
CREATE TABLE search_hits (
    id           bigserial PRIMARY KEY,
    engine       text NOT NULL CHECK (engine IN ('api', 'web')),
    keyword_id   bigint,
    video_id     text,
    channel_id   text,
    title        text,
    published_at timestamptz,
    found_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX search_hits_engine_found ON search_hits (engine, found_at);
CREATE INDEX search_hits_channel ON search_hits (channel_id);

CREATE TABLE candidates (
    channel_id  text PRIMARY KEY,
    first_found timestamptz NOT NULL DEFAULT now(),
    last_found  timestamptz NOT NULL DEFAULT now(),
    hits        integer NOT NULL DEFAULT 1,
    sources     text[] NOT NULL DEFAULT '{}',        -- api_search, web_search, graph, commenter
    status      text NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'checked', 'listed', 'no_match')),
    checked_at  timestamptz,
    decision    text                                 -- core, mention, no_match, and why
);
CREATE INDEX candidates_status ON candidates (status, hits DESC);

CREATE TABLE channel_links_graph (
    from_channel text NOT NULL,
    to_channel   text NOT NULL,
    relation     text NOT NULL CHECK (relation IN ('featured', 'link', 'mention', 'collab', 'commenter')),
    first_seen   timestamptz NOT NULL DEFAULT now(),
    last_seen    timestamptz NOT NULL DEFAULT now(),
    weight       integer NOT NULL DEFAULT 1,
    PRIMARY KEY (from_channel, to_channel, relation)
);
CREATE INDEX channel_links_graph_to ON channel_links_graph (to_channel);

-- ----------------------------------------------------------------- privacy

-- Keys are HMAC-SHA256(secret, kind ‖ id): the list itself does not reveal
-- who or what was erased.
CREATE TABLE suppression_list (
    kind            text NOT NULL CHECK (kind IN ('author', 'channel', 'video', 'comment', 'post')),
    key             bytea NOT NULL,
    reason          text NOT NULL,
    request_id      bigint,
    created_at      timestamptz NOT NULL DEFAULT now(),
    override_at     timestamptz,                     -- an admin lifted it (with a reason)
    override_reason text,
    PRIMARY KEY (kind, key)
);

CREATE TABLE erasure_requests (
    id           bigserial PRIMARY KEY,
    kind         text NOT NULL CHECK (kind IN ('author', 'channel', 'video', 'comment', 'post')),
    target       text,                               -- cleared once processed; the key remains
    target_key   bytea NOT NULL,
    requested_by text,
    received_at  timestamptz NOT NULL DEFAULT now(),
    status       text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'done', 'failed')),
    done_at      timestamptz,
    error        text
);
CREATE INDEX erasure_requests_pending ON erasure_requests (received_at) WHERE status = 'pending';

-- No personal data: request IDs, steps and row counts only.
CREATE TABLE erasure_audit (
    id         bigserial PRIMARY KEY,
    request_id bigint NOT NULL,
    at         timestamptz NOT NULL DEFAULT now(),
    step       text NOT NULL,
    rows       bigint NOT NULL DEFAULT 0
);

-- -------------------------------------------------------------- operations

-- The schedule is the job queue: one row per thing to check, with its next
-- due time and priority (P0 new uploads … P4 enrichment). Workers lease due
-- rows with FOR UPDATE SKIP LOCKED; "check now" sets due_at to now().
CREATE TABLE schedule (
    kind        text NOT NULL,
    entity_id   text NOT NULL,
    channel_id  text,                                -- one job per channel at a time
    priority    smallint NOT NULL CHECK (priority BETWEEN 0 AND 4),
    due_at      timestamptz NOT NULL,
    lease_until timestamptz,
    lease_owner text,
    attempts    integer NOT NULL DEFAULT 0,
    last_run    timestamptz,
    last_ok     timestamptz,
    last_error  text,
    PRIMARY KEY (kind, entity_id)
);
CREATE INDEX schedule_due ON schedule (priority, due_at);
CREATE INDEX schedule_channel ON schedule (channel_id) WHERE channel_id IS NOT NULL;

-- Official API quota per day (the day resets at midnight Pacific time).
CREATE TABLE quota_ledger (
    day        date PRIMARY KEY,
    units_used integer NOT NULL DEFAULT 0,
    units_cap  integer NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Proxy state without credentials: proxy_key is a hash of the proxy URL.
CREATE TABLE proxy_health (
    proxy_key      text PRIMARY KEY,
    label          text NOT NULL,                    -- host:port, no user or password
    subnet         text NOT NULL,
    proxy_kind     text,
    state          text NOT NULL,
    cooldown_until timestamptz,
    requests_24h   bigint NOT NULL DEFAULT 0,
    blocks_24h     bigint NOT NULL DEFAULT 0,
    last_block_at  timestamptz,
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE parse_failures (
    id          bigserial PRIMARY KEY,
    at          timestamptz NOT NULL DEFAULT now(),
    endpoint    text NOT NULL,
    entity_kind text,
    entity_id   text,
    error       text NOT NULL,
    raw_ref     text,                                -- raw response file, kept 3 days
    resolved_at timestamptz
);
CREATE INDEX parse_failures_open ON parse_failures (at) WHERE resolved_at IS NULL;

CREATE TABLE field_fill_rates (
    window_start timestamptz NOT NULL,
    endpoint     text NOT NULL,
    field        text NOT NULL,
    seen         bigint NOT NULL,
    present      bigint NOT NULL,
    PRIMARY KEY (window_start, endpoint, field)
);

CREATE TABLE capacity_reports (
    at              timestamptz PRIMARY KEY,
    supply_per_hour double precision NOT NULL,
    demand_per_hour double precision NOT NULL,
    stretch         real NOT NULL,                   -- interval multiplier for C, D, M
    debt            jsonb,                           -- freshness debt per tier
    recommended_ips integer,
    detail          jsonb
);

-- Runtime settings: kill switches, the speed multiplier, retention (off).
CREATE TABLE settings (
    key        text PRIMARY KEY,
    value      jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
