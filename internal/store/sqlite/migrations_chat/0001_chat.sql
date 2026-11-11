-- One chat, whole: its journal of events (the source of truth) and everything built from it. The
-- chat's id is the SHA-256 of its token; the token itself is never stored.
--
-- events: one row per change, chained per origin and signed by the node that accepted it
-- (internal/events). payload is what the author sent and signed (canonical JSON), assigned what
-- the node issued or wrote (ids, images of rows). The tables below it are projections: they can be
-- rebuilt from events (`ccqc db rebuild`) and `ccqc db verify` checks the chains and signatures.
CREATE TABLE chat (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    chat_id    TEXT    NOT NULL,
    name       TEXT    NOT NULL,
    created_at TEXT    NOT NULL,
    lamport    INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE events (
    origin     TEXT    NOT NULL,
    seq        INTEGER NOT NULL,
    prev       TEXT    NOT NULL DEFAULT '',
    lamport    INTEGER NOT NULL,
    kind       TEXT    NOT NULL,
    payload    TEXT    NOT NULL,
    author_pub TEXT    NOT NULL DEFAULT '',
    sig        TEXT    NOT NULL DEFAULT '',
    assigned   TEXT    NOT NULL DEFAULT '{}',
    ts         TEXT    NOT NULL,
    node_sig   TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (origin, seq)
) WITHOUT ROWID;

CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) WITHOUT ROWID;

-- A nick is bound to a key: key_hash is the hash of the secret of a nick that joins by it, pubkey
-- the Ed25519 key of one that joins by signature. seen: last message id delivered live before
-- the nick went offline (the node's own state, not derived from events).
CREATE TABLE nicks (
    nick        TEXT PRIMARY KEY,
    key_hash    TEXT NOT NULL,
    seen        INTEGER NOT NULL DEFAULT 0,
    pinned_role TEXT NOT NULL DEFAULT '',
    pubkey      TEXT NOT NULL DEFAULT ''
) WITHOUT ROWID;

CREATE TABLE rooms (
    room       TEXT PRIMARY KEY,
    topic      TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL DEFAULT ''
) WITHOUT ROWID;

CREATE TABLE room_members (
    room TEXT NOT NULL REFERENCES rooms (room) ON DELETE CASCADE,
    nick TEXT NOT NULL,
    PRIMARY KEY (room, nick)
) WITHOUT ROWID;

-- id is per chat. thread: "r:<room>" or "d:<nick-a>|<nick-b>" (sorted pair).
CREATE TABLE messages (
    id            INTEGER PRIMARY KEY,
    thread        TEXT    NOT NULL,
    room          TEXT    NOT NULL,
    from_nick     TEXT    NOT NULL,
    from_role     TEXT    NOT NULL DEFAULT '',
    to_nick       TEXT    NOT NULL DEFAULT '',
    text          TEXT    NOT NULL,
    reply_to      INTEGER NOT NULL DEFAULT 0,
    ts            TEXT    NOT NULL,
    client_msg_id TEXT    NOT NULL DEFAULT '',
    owner         INTEGER NOT NULL DEFAULT 0,
    origin        TEXT    NOT NULL DEFAULT '',
    lamport       INTEGER NOT NULL DEFAULT 0,
    author_pub    TEXT    NOT NULL DEFAULT '',
    sig           TEXT    NOT NULL DEFAULT '',
    payload       TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX messages_by_thread ON messages (thread, id);
-- Idempotent send: a sender's retry carries the same client_msg_id and must not store a second
-- message. The unique index covers non-empty ids only.
CREATE UNIQUE INDEX messages_by_client_id ON messages (from_nick, client_msg_id) WHERE client_msg_id <> '';

-- The task board: sprints, tasks, the status process and the journal of its changes. Nothing is
-- deleted physically: tasks and sprints are flagged.
CREATE TABLE board_sprints (
    id         INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL,
    goal       TEXT    NOT NULL DEFAULT '',
    state      TEXT    NOT NULL,
    created_at TEXT    NOT NULL,
    started_at TEXT    NOT NULL DEFAULT '',
    closed_at  TEXT    NOT NULL DEFAULT '',
    deleted    INTEGER NOT NULL DEFAULT 0
);

-- At most one active sprint, enforced by the database.
CREATE UNIQUE INDEX board_one_active_sprint ON board_sprints (state) WHERE state = 'active' AND deleted = 0;

CREATE TABLE board_tasks (
    id          INTEGER PRIMARY KEY,
    title       TEXT    NOT NULL,
    description TEXT    NOT NULL DEFAULT '',
    status      TEXT    NOT NULL,
    sprint      INTEGER NOT NULL DEFAULT 0,
    assignee    TEXT    NOT NULL DEFAULT '',
    priority    INTEGER NOT NULL DEFAULT 0,
    created_by  TEXT    NOT NULL,
    created_at  TEXT    NOT NULL,
    updated_at  TEXT    NOT NULL,
    deleted     INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX board_tasks_by_sprint ON board_tasks (sprint, deleted, status);
CREATE INDEX board_tasks_by_assignee ON board_tasks (assignee, deleted);

CREATE TABLE board_workflow (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    json       TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE board_log (
    id          INTEGER PRIMARY KEY,
    ts          TEXT    NOT NULL,
    actor       TEXT    NOT NULL,
    actor_owner INTEGER NOT NULL DEFAULT 0,
    kind        TEXT    NOT NULL,
    entity      TEXT    NOT NULL,
    entity_id   INTEGER NOT NULL DEFAULT 0,
    before_json TEXT    NOT NULL DEFAULT '{}',
    after_json  TEXT    NOT NULL DEFAULT '{}',
    reverts     INTEGER NOT NULL DEFAULT 0,
    note        TEXT    NOT NULL DEFAULT '',
    origin      TEXT    NOT NULL DEFAULT '',
    lamport     INTEGER NOT NULL DEFAULT 0,
    actor_grant TEXT    NOT NULL DEFAULT '',
    author_pub  TEXT    NOT NULL DEFAULT '',
    sig         TEXT    NOT NULL DEFAULT '',
    payload     TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX board_log_by_entity ON board_log (entity, entity_id, id);

-- Comments on tasks. Append-only: hiding sets deleted, nothing is removed.
CREATE TABLE board_comments (
    id           INTEGER PRIMARY KEY,
    task_id      INTEGER NOT NULL,
    ts           TEXT    NOT NULL,
    author       TEXT    NOT NULL,
    author_owner INTEGER NOT NULL DEFAULT 0,
    text         TEXT    NOT NULL,
    deleted      INTEGER NOT NULL DEFAULT 0,
    origin       TEXT    NOT NULL,
    lamport      INTEGER NOT NULL,
    author_pub   TEXT    NOT NULL DEFAULT '',
    sig          TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX board_comments_by_task ON board_comments (task_id, id);

-- The document tree: pages by path, every write a new version. docs is the current state (a
-- cache of the journal); doc_versions is the journal, append-only.
CREATE TABLE docs (
    path         TEXT    PRIMARY KEY,
    title        TEXT    NOT NULL,
    body         TEXT    NOT NULL,
    version      INTEGER NOT NULL,
    author       TEXT    NOT NULL,
    author_owner INTEGER NOT NULL DEFAULT 0,
    ts           TEXT    NOT NULL,
    deleted      INTEGER NOT NULL DEFAULT 0
) WITHOUT ROWID;

CREATE TABLE doc_versions (
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
    author_pub   TEXT    NOT NULL DEFAULT '',
    sig          TEXT    NOT NULL DEFAULT '',
    payload      TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (path, version)
) WITHOUT ROWID;

-- The settings of this chat (see `ccqc config`).
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) WITHOUT ROWID;
