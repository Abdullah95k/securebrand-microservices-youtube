-- Fields the channel and video collection needs (plan §8.4, §8.8).
ALTER TABLE channels ADD COLUMN missed_checks smallint NOT NULL DEFAULT 0;  -- 2 misses → removed
ALTER TABLE channels ADD COLUMN joined_text text;                           -- as shown ("Joined …"), for audits
ALTER TABLE channels ADD COLUMN family_safe boolean;
ALTER TABLE videos ADD COLUMN published_text text;                          -- as shown, for audits
ALTER TABLE videos ADD COLUMN family_safe boolean;
ALTER TABLE videos ADD COLUMN caption_langs text[];
ALTER TABLE videos ADD COLUMN chapters jsonb;
