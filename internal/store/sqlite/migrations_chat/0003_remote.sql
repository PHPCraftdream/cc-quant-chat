-- Events of other nodes applied to the projections (federation, stage 3b).
--
-- events.remote: 1 for an event that came from another node. A rebuild replays the local ones as
-- they were written and the remote ones through the rules of foreign events, in the order `ord`.
-- entity_refs: the global name of what an event created, (origin, seq) of that event, for the local
-- number this node shows it under: a message's #id. A reference to something made elsewhere
-- travels as (origin, seq) and is turned into the local number here.
-- rooms.lamport/origin: the event that made the room; of two nodes that made the same room the
-- smaller (lamport, origin) is kept, so that both end with one.
-- entity_refs.num: the number the node that made the thing gave it (a task of another node is shown
-- as T-abcd-<num>).
-- board_stamps: the (lamport, origin) of the event that last set a field of a task, a sprint, a
-- comment or the workflow: of two concurrent writes of one field the larger stamp wins on every node.
-- Two nodes may both start a sprint, so the index that allowed one active sprint is dropped: a
-- merge does not reject what the history already holds.
ALTER TABLE events ADD COLUMN remote INTEGER NOT NULL DEFAULT 0;

CREATE TABLE entity_refs (
    kind   TEXT    NOT NULL,
    id     INTEGER NOT NULL,
    origin TEXT    NOT NULL,
    seq    INTEGER NOT NULL,
    num    INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (kind, id)
) WITHOUT ROWID;

CREATE UNIQUE INDEX entity_refs_by_event ON entity_refs (kind, origin, seq);

ALTER TABLE rooms ADD COLUMN lamport INTEGER NOT NULL DEFAULT 0;
ALTER TABLE rooms ADD COLUMN origin TEXT NOT NULL DEFAULT '';

CREATE TABLE board_stamps (
    kind    TEXT    NOT NULL,
    id      INTEGER NOT NULL,
    field   TEXT    NOT NULL,
    lamport INTEGER NOT NULL,
    origin  TEXT    NOT NULL,
    PRIMARY KEY (kind, id, field)
) WITHOUT ROWID;

DROP INDEX board_one_active_sprint;
