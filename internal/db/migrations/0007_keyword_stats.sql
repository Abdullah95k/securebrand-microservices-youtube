-- Keyword precision (plan §5.1): per keyword and day, how many items it
-- matched and how many of those another Iraq signal confirmed (a second
-- keyword, or a core channel).
CREATE TABLE keyword_stats (
    keyword_id bigint NOT NULL,
    day        date NOT NULL,
    matches    integer NOT NULL,
    confirmed  integer NOT NULL,
    PRIMARY KEY (keyword_id, day)
);
