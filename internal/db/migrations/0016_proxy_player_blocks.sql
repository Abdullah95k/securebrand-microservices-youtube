-- "ytcollect proxies" splits an IP's blocks into page and player blocks
-- (player blocks are expected and never count towards quarantine).
-- Additions only: blocks_24h stays the total since the worker started.
ALTER TABLE proxy_health ADD COLUMN player_blocks bigint NOT NULL DEFAULT 0;
