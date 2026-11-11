-- Chats are keyed by SHA-256(token); the token itself is never stored.
CREATE TABLE chats (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at TEXT NOT NULL
);

-- A nick is bound to a key; only its hash is stored. seen: last message id
-- delivered live before the nick went offline.
CREATE TABLE nicks (
    chat_id  TEXT NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    nick     TEXT NOT NULL,
    key_hash TEXT NOT NULL,
    seen     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (chat_id, nick)
) WITHOUT ROWID;

CREATE TABLE rooms (
    chat_id    TEXT NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    room       TEXT NOT NULL,
    topic      TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (chat_id, room)
) WITHOUT ROWID;

CREATE TABLE room_members (
    chat_id TEXT NOT NULL,
    room    TEXT NOT NULL,
    nick    TEXT NOT NULL,
    PRIMARY KEY (chat_id, room, nick),
    FOREIGN KEY (chat_id, room) REFERENCES rooms (chat_id, room) ON DELETE CASCADE
) WITHOUT ROWID;

-- id is per chat. thread: "r:<room>" or "d:<nick-a>|<nick-b>" (sorted pair).
CREATE TABLE messages (
    chat_id   TEXT    NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    id        INTEGER NOT NULL,
    thread    TEXT    NOT NULL,
    room      TEXT    NOT NULL,
    from_nick TEXT    NOT NULL,
    from_role TEXT    NOT NULL DEFAULT '',
    to_nick   TEXT    NOT NULL DEFAULT '',
    text      TEXT    NOT NULL,
    reply_to  INTEGER NOT NULL DEFAULT 0,
    ts        TEXT    NOT NULL,
    PRIMARY KEY (chat_id, id)
) WITHOUT ROWID;

CREATE INDEX messages_by_thread ON messages (chat_id, thread, id);
CREATE INDEX messages_by_from ON messages (chat_id, from_nick, id);
CREATE INDEX messages_by_to ON messages (chat_id, to_nick, id);
