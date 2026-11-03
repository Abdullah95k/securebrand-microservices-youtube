-- Posts: the browse params of a post's own page (its comments are there),
-- and when its comments were first read in full.
ALTER TABLE posts ADD COLUMN detail_params text;
ALTER TABLE posts ADD COLUMN comments_read_at timestamptz;
ALTER TABLE posts ADD COLUMN has_image boolean;
ALTER TABLE posts ADD COLUMN has_poll boolean;
ALTER TABLE posts ADD COLUMN has_video boolean;
ALTER TABLE posts ADD COLUMN published_text text;
