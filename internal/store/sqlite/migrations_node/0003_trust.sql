-- Trust between nodes (federation, stage 4; docs/design-trust.md).
--
-- peers.trust is what this node accepts from the node: read (nothing), member (the events of that
-- node's own source) or trusted (also the sources it brings). The nodes listed before levels existed
-- were added by hand and worked as trusted; they stay trusted. A value that is none of the three is
-- made trusted, as it was treated.
-- peers.state: active, or pending (a node that has used an invitation and whose owner has not yet
-- confirmed the code: nothing is exchanged with it); pending_until: when a pending node is dropped.
ALTER TABLE peers ADD COLUMN state TEXT NOT NULL DEFAULT 'active';
ALTER TABLE peers ADD COLUMN pending_until TEXT NOT NULL DEFAULT '';
UPDATE peers SET trust = 'trusted' WHERE trust NOT IN ('read', 'member', 'trusted');
