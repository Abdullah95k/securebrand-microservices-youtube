-- Measured cost (REQ-010 fix 5, standard D5), read by "ytcollect report".
--
-- Every job counts what it used (requests, bytes on the wire, block
-- signals, run time). The worker adds it up per hour and job kind, and per
-- day (Iraq time) and channel for jobs that belong to a monitored channel.
-- One row per channel per day keeps this small at 10,000 channels (about
-- 0.5 GB a year). Kept like everything else.
CREATE TABLE usage_hourly (
    hour     timestamptz NOT NULL,                  -- start of the hour
    kind     text NOT NULL,                         -- job kind
    jobs     bigint NOT NULL DEFAULT 0,
    failures bigint NOT NULL DEFAULT 0,
    requests bigint NOT NULL DEFAULT 0,              -- HTTP requests sent, retries and failovers included
    bytes    bigint NOT NULL DEFAULT 0,              -- on the wire, both ways
    blocks   bigint NOT NULL DEFAULT 0,              -- block signals met
    run_ms   bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (hour, kind)
);

CREATE TABLE usage_channel_daily (
    day               date NOT NULL,                -- Iraq time (UTC+3)
    channel_id        text NOT NULL,
    jobs              bigint NOT NULL DEFAULT 0,
    failures          bigint NOT NULL DEFAULT 0,
    requests          bigint NOT NULL DEFAULT 0,
    bytes             bigint NOT NULL DEFAULT 0,
    blocks            bigint NOT NULL DEFAULT 0,
    run_ms            bigint NOT NULL DEFAULT 0,
    backfill_requests bigint NOT NULL DEFAULT 0,     -- the one-off backfill's share of the above
    backfill_bytes    bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (day, channel_id)
);

-- The database's size, sampled hourly by the scheduler: growth per day.
CREATE TABLE db_sizes (
    at     timestamptz PRIMARY KEY,
    bytes  bigint NOT NULL,
    tables jsonb NOT NULL DEFAULT '{}'               -- the largest tables, bytes with indexes
);
