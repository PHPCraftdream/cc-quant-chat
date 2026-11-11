-- Inviting a node (federation, stage 4; docs/design-trust.md section 2).
--
-- invites: the invitations of kind 'node' (the same table as in a chat: invite.Schema), the public key
-- of the one-time key, the lifetime and the scope (what is given to the node that uses it, as JSON).
-- peers, for a node that used an invitation and is not confirmed yet (state = 'pending'): the code
-- the owners compare (pending_code; shown only to the owner), wrong codes entered (pending_tries),
-- and who has confirmed it: this node's owner (confirmed_local) and the other's (confirmed_remote).
-- pending_chats: the chats the other node offers to share, with their names; they are made here
-- only when the node becomes active.
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

ALTER TABLE peers ADD COLUMN pending_code TEXT NOT NULL DEFAULT '';
ALTER TABLE peers ADD COLUMN pending_tries INTEGER NOT NULL DEFAULT 0;
ALTER TABLE peers ADD COLUMN confirmed_local INTEGER NOT NULL DEFAULT 0;
ALTER TABLE peers ADD COLUMN confirmed_remote INTEGER NOT NULL DEFAULT 0;

CREATE TABLE pending_chats (
    origin  TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    name    TEXT NOT NULL,
    PRIMARY KEY (origin, chat_id)
) WITHOUT ROWID;
