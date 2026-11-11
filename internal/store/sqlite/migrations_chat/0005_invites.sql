-- Invitations (docs/design-invites.md): a one-time key per entry into the chat; only its public key
-- is kept here, never the secret. This is the state of this node (tries, used, burnt), not a
-- projection of the journal; the journal has the events (invite.issue, invite.revoke, ...) and the
-- nick that an invitation registered carries which one it was.
-- The chat's meta row `mode` is 'invite' for a chat made by `ccqc chat new` (its id is random, it has
-- no token); a chat without it is an older one whose id is the hash of its token.
CREATE TABLE invites (
    id         TEXT    PRIMARY KEY,
    pub        TEXT    NOT NULL,
    kind       TEXT    NOT NULL,
    scope      TEXT    NOT NULL DEFAULT '',
    expires_at INTEGER NOT NULL,
    tries      INTEGER NOT NULL DEFAULT 0,
    state      TEXT    NOT NULL DEFAULT 'open',
    issued_by  TEXT    NOT NULL DEFAULT '',
    issued_at  INTEGER NOT NULL,
    used_at    INTEGER NOT NULL DEFAULT 0,
    used_by    TEXT    NOT NULL DEFAULT ''
) WITHOUT ROWID;
