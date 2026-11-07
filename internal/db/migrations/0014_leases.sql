-- REQ-023: leases for work only one process may do at a time (the relay).
-- Session advisory locks don't work through PgBouncer's transaction
-- pooling (the lock stays with a shared server session), so the holder
-- renews a row instead, in short transactions. Additions only.
CREATE TABLE leases (
    name   text PRIMARY KEY,
    holder text NOT NULL,          -- the process: host/pid/random
    until  timestamptz NOT NULL    -- the lease ends then unless renewed
);
