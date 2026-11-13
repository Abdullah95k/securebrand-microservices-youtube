-- Facebook discovery (the owner, D-065, 13 November: "activate Facebook
-- discovery"): the Facebook links found on listed channels' About panels
-- and descriptions, each sent once to the Facebook collector. An addition;
-- nothing others use changes.
CREATE TABLE fb_candidates (
    ref          text PRIMARY KEY,      -- the link exactly as found
    channel_id   text NOT NULL,         -- the first listed channel it was found on
    evidence_url text NOT NULL,         -- that channel's page
    first_seen   timestamptz NOT NULL,
    sent_at      timestamptz,           -- NULL: not sent yet
    result       text,                  -- fbs's answer (queued, on_list, added, …) or rejected
    attempts     integer NOT NULL DEFAULT 0,
    last_error   text
);
CREATE INDEX fb_candidates_unsent ON fb_candidates (first_seen) WHERE sent_at IS NULL;
CREATE INDEX fb_candidates_channel ON fb_candidates (channel_id);
