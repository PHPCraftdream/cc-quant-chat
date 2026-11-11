-- Groundwork for merging brokers later (nothing here changes behavior) and task comments.
--
-- origins: this node's identity, the first 16 bytes of SHA-256 of its public key (hex); the key
-- itself lives in a file outside the database. Journal rows say which node wrote them (origin)
-- and carry a per-chat logical clock (lamport, kept in chats.lamport).
CREATE TABLE origins (
    origin     TEXT PRIMARY KEY,
    pubkey     BLOB NOT NULL,
    created_at TEXT NOT NULL
);

ALTER TABLE chats ADD COLUMN lamport INTEGER NOT NULL DEFAULT 0;

ALTER TABLE messages ADD COLUMN origin TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN lamport INTEGER NOT NULL DEFAULT 0;

ALTER TABLE board_log ADD COLUMN origin TEXT NOT NULL DEFAULT '';
ALTER TABLE board_log ADD COLUMN lamport INTEGER NOT NULL DEFAULT 0;
-- A grant the actor held when it made the change (reserved; empty for now).
ALTER TABLE board_log ADD COLUMN actor_grant TEXT NOT NULL DEFAULT '';

-- Comments on tasks. Append-only: hiding sets deleted, nothing is removed.
CREATE TABLE board_comments (
    chat_id      TEXT    NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    id           INTEGER NOT NULL,
    task_id      INTEGER NOT NULL,
    ts           TEXT    NOT NULL,
    author       TEXT    NOT NULL,
    author_owner INTEGER NOT NULL DEFAULT 0,
    text         TEXT    NOT NULL,
    deleted      INTEGER NOT NULL DEFAULT 0,
    origin       TEXT    NOT NULL,
    lamport      INTEGER NOT NULL,
    PRIMARY KEY (chat_id, id)
) WITHOUT ROWID;

CREATE INDEX board_comments_by_task ON board_comments (chat_id, task_id, id);
