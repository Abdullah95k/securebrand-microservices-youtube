-- A check-now that arrives while a job is running is remembered here and
-- applied when the run ends, so the job runs again instead of waiting for
-- its normal interval.
ALTER TABLE schedule ADD COLUMN rearm_at timestamptz;

-- Channels you add by handle or URL wait here until a worker resolves them
-- to a channel ID (job kind "resolve"); the row stays as a record of who
-- added what and why.
CREATE TABLE channel_inputs (
    input       text PRIMARY KEY,                    -- as given: @handle, URL or UC… ID
    kind        text NOT NULL DEFAULT 'core' CHECK (kind IN ('core', 'mention')),
    tier        text NOT NULL DEFAULT 'C' CHECK (tier IN ('A', 'B', 'C', 'D', 'M')),
    added_by    text NOT NULL DEFAULT 'admin',
    reason      text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    channel_id  text,                                -- once resolved
    resolved_at timestamptz,
    error       text                                 -- why it could not be resolved
);
