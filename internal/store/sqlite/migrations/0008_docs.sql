-- The chat's document tree: pages by path, every write a new version. docs is the current state
-- (a cache of the journal); doc_versions is the journal, append-only.
CREATE TABLE docs (
    chat_id      TEXT    NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    path         TEXT    NOT NULL,
    title        TEXT    NOT NULL,
    body         TEXT    NOT NULL,
    version      INTEGER NOT NULL,
    author       TEXT    NOT NULL,
    author_owner INTEGER NOT NULL DEFAULT 0,
    ts           TEXT    NOT NULL,
    deleted      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (chat_id, path)
) WITHOUT ROWID;

CREATE TABLE doc_versions (
    chat_id      TEXT    NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    path         TEXT    NOT NULL,
    version      INTEGER NOT NULL,
    ts           TEXT    NOT NULL,
    author       TEXT    NOT NULL,
    author_owner INTEGER NOT NULL DEFAULT 0,
    title        TEXT    NOT NULL,
    body         TEXT    NOT NULL,
    note         TEXT    NOT NULL DEFAULT '',
    deleted      INTEGER NOT NULL DEFAULT 0,
    origin       TEXT    NOT NULL,
    lamport      INTEGER NOT NULL,
    PRIMARY KEY (chat_id, path, version)
) WITHOUT ROWID;
