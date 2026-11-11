-- Limits per source and quarantine (federation, stage 4; docs/design-trust.md sections 4 and 5).
--
-- quarantine: a source whose new events are not taken (by the owner's decision, or automatically after
-- a breach of the limits); what was taken stays. via: the neighbor that brought it, for one that is
-- not a neighbor itself. A fork (flagged_origins) is a quarantine too and is listed with it.
-- peer_stats: per source and day what was taken (events, bytes) and how many times a limit was
-- breached; three breaches in a day make a quarantine, so the count survives a restart.
CREATE TABLE quarantine (
    origin TEXT PRIMARY KEY,
    reason TEXT NOT NULL,
    since  TEXT NOT NULL,
    by     TEXT NOT NULL,
    via    TEXT NOT NULL DEFAULT ''
) WITHOUT ROWID;

CREATE TABLE peer_stats (
    origin     TEXT    NOT NULL,
    day        TEXT    NOT NULL,
    events     INTEGER NOT NULL DEFAULT 0,
    bytes      INTEGER NOT NULL DEFAULT 0,
    violations INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (origin, day)
) WITHOUT ROWID;
