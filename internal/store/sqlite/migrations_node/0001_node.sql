-- The database of the node: what belongs to this broker rather than to one chat. Each chat has a
-- database of its own (chats/<chat id>.db); here is only the index of them.
--
-- origins: the node's identity, the first 16 bytes of SHA-256 of its public key (hex); the key
-- itself lives in a file outside the database. owner: the human who owns the broker; one row, the
-- Ed25519 public key. admin_log: every administrator attempt, refused ones included (no secrets).
-- settings: broker-wide settings; the settings of a chat live in its own database.
CREATE TABLE origins (
    origin     TEXT PRIMARY KEY,
    pubkey     BLOB NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE owner (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    pubkey     BLOB NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE admin_log (
    id     INTEGER PRIMARY KEY AUTOINCREMENT,
    ts     TEXT NOT NULL,
    op     TEXT NOT NULL,
    args   TEXT NOT NULL,
    result TEXT NOT NULL,
    origin TEXT NOT NULL DEFAULT ''
);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) WITHOUT ROWID;

-- The chats of this node: the name for people and the file name inside chats/. A file in chats/
-- that is not listed here is picked up at start.
CREATE TABLE chats (
    chat_id    TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    file       TEXT NOT NULL,
    created_at TEXT NOT NULL
) WITHOUT ROWID;

-- Small facts about the node's data: whether the single database of earlier versions has been
-- laid out into chat files, and which chats are done.
CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) WITHOUT ROWID;
