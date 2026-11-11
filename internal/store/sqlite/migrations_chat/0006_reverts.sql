-- Reverting the contribution of a node (federation, stage 4; docs/design-trust.md section 6).
--
-- The reverts in force are read from the journal (peer.revert, peer.unrevert) every time an event of
-- another node is taken and every time the projections are built; this index keeps that read from
-- going through the whole journal.
CREATE INDEX events_reverts ON events (ord) WHERE kind IN ('peer.revert', 'peer.unrevert');
