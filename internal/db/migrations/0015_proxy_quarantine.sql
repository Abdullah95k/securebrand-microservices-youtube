-- The quarantine rule (11 November, the owner): a quarantine outlives a
-- restart and is lifted by "ytcollect proxies release". Additions only.
-- quarantined_since is written by the worker each minute (NULL when the IP
-- isn't quarantined); released_at by the release command. A row from before
-- this migration has neither, so an IP the old rule quarantined is never
-- restored.
ALTER TABLE proxy_health ADD COLUMN quarantined_since timestamptz;
ALTER TABLE proxy_health ADD COLUMN released_at timestamptz;
