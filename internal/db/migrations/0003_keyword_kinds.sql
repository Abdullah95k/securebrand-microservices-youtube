-- Hashtags are a keyword kind too (plan §5.1).
ALTER TABLE keywords DROP CONSTRAINT keywords_kind_check;
ALTER TABLE keywords ADD CONSTRAINT keywords_kind_check
    CHECK (kind IN ('place', 'person', 'org', 'topic', 'brand', 'hashtag', 'term'));

-- The seed list's group (the governorate a place belongs to).
ALTER TABLE keywords ADD COLUMN grp text;
