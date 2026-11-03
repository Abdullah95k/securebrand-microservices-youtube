-- Discovery (plan §5): keyword priority and expiry, the search rotation,
-- and manual tier overrides.
ALTER TABLE keywords ADD COLUMN priority smallint NOT NULL DEFAULT 0;  -- ≥ 1: API sweeps every 3 h, website hourly
ALTER TABLE keywords ADD COLUMN expires_at timestamptz;                -- trending suggestions expire after 7 days

-- One row per search term (a keyword spelling), with when each engine last
-- searched it, so a run asks only for what is new since the last one.
CREATE TABLE search_terms (
    term       text PRIMARY KEY,                     -- normalized spelling
    query      text NOT NULL,                        -- the spelling as searched
    keyword_id bigint NOT NULL,
    last_web   timestamptz,
    last_api   timestamptz,
    web_hits   bigint NOT NULL DEFAULT 0,
    api_hits   bigint NOT NULL DEFAULT 0
);

-- A tier set by hand is kept by the weekly recompute.
ALTER TABLE channels ADD COLUMN tier_manual boolean NOT NULL DEFAULT false;

CREATE INDEX search_hits_video ON search_hits (video_id);
