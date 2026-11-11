-- Exchange of the journal with other nodes (federation, stage 3).
--
-- ord: the order in which this node applied its events, its own and those that came from other
-- nodes. A rebuild replays in this order, so the numbers a node gave to what it received come out
-- the same. Events written before this migration are ordered by (lamport, origin, seq).
-- hidden: for an event kept without its contents (it came redacted), the hashes of the four
-- contents as JSON; empty for a whole event.
ALTER TABLE events ADD COLUMN ord INTEGER NOT NULL DEFAULT 0;
ALTER TABLE events ADD COLUMN hidden TEXT NOT NULL DEFAULT '';

UPDATE events SET ord = r.n
FROM (SELECT origin, seq, ROW_NUMBER() OVER (ORDER BY lamport, origin, seq) AS n FROM events) AS r
WHERE events.origin = r.origin AND events.seq = r.seq;

CREATE UNIQUE INDEX events_by_ord ON events (ord);

-- Who the nicks of other nodes are: (origin, nick) -> the key the node registered for it. A message
-- from that nick must be signed by that key.
CREATE TABLE remote_nicks (
    origin TEXT NOT NULL,
    nick   TEXT NOT NULL,
    pub    TEXT NOT NULL,
    PRIMARY KEY (origin, nick)
) WITHOUT ROWID;
