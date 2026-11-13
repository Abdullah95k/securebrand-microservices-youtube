-- Fix 21 (images): community post images, for analysis (the owner, 13
-- November: "also make sure all platforms media is being analyzed").
--
-- posts.image_urls: the post's attached images, the largest size of each,
-- in order. Empty for a post without images, or read before this change.
-- media gains the kind post_image: one row per distinct image of a post
-- (entity_id is the post's ID, channel_id its channel). Both are additions;
-- nothing others use changes.
ALTER TABLE posts ADD COLUMN image_urls text[] NOT NULL DEFAULT '{}';

ALTER TABLE media DROP CONSTRAINT media_kind_check;
ALTER TABLE media ADD CONSTRAINT media_kind_check
    CHECK (kind IN ('thumb', 'avatar', 'banner', 'author_avatar', 'post_image'));
