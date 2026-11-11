-- Events of other nodes applied to the projections (federation, stage 3b).
--
-- events.remote: 1 for an event that came from another node. A rebuild replays the local ones as
-- they were written and the remote ones through the rules of foreign events, in the order `ord`.
-- entity_refs: the global name of what an event created, (origin, seq) of that event, for the local
-- number this node shows it under: a message's #id. A reference to something made elsewhere
-- travels as (origin, seq) and is turned into the local number here.
-- rooms.lamport/origin: the event that made the room; of two nodes that made the same room the
-- smaller (lamport, origin) is kept, so that both end with one.
ALTER TABLE events ADD COLUMN remote INTEGER NOT NULL DEFAULT 0;

CREATE TABLE entity_refs (
    kind   TEXT    NOT NULL,
    id     INTEGER NOT NULL,
    origin TEXT    NOT NULL,
    seq    INTEGER NOT NULL,
    PRIMARY KEY (kind, id)
) WITHOUT ROWID;

CREATE UNIQUE INDEX entity_refs_by_event ON entity_refs (kind, origin, seq);

ALTER TABLE rooms ADD COLUMN lamport INTEGER NOT NULL DEFAULT 0;
ALTER TABLE rooms ADD COLUMN origin TEXT NOT NULL DEFAULT '';
