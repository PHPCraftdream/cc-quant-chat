-- Sources brought by a neighbor (federation, stage 4; docs/design-trust.md section 3).
--
-- origin_sources: a source (the origin of events) that is not a neighbor of this node and whose
-- events came through a trusted neighbor (via). The number of them per neighbor is limited.
CREATE TABLE origin_sources (
    origin     TEXT NOT NULL,
    via        TEXT NOT NULL,
    first_seen TEXT NOT NULL,
    PRIMARY KEY (origin, via)
) WITHOUT ROWID;
