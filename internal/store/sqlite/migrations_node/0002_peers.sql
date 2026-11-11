-- Other nodes this node exchanges the journals of chats with (federation, stage 3).
--
-- peers: a node added by the owner (`ccqc peer add`): its origin, where to reach it ('' if only it
-- connects to us), the level of trust (trusted for one added by hand; levels come with stage 4) and
-- its public key once seen. peer_chats: the chats shared with a node; without a row nothing of a
-- chat is given to it or taken from it. flagged_origins: a node that was caught writing two
-- different events with one (origin, seq): its events are no longer accepted.
CREATE TABLE peers (
    origin   TEXT PRIMARY KEY,
    addr     TEXT NOT NULL DEFAULT '',
    trust    TEXT NOT NULL DEFAULT 'trusted',
    pubkey   BLOB,
    added_at TEXT NOT NULL
) WITHOUT ROWID;

CREATE TABLE peer_chats (
    origin  TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    PRIMARY KEY (origin, chat_id)
) WITHOUT ROWID;

CREATE TABLE flagged_origins (
    origin   TEXT PRIMARY KEY,
    reason   TEXT NOT NULL,
    evidence TEXT NOT NULL DEFAULT '',
    at       TEXT NOT NULL
) WITHOUT ROWID;
