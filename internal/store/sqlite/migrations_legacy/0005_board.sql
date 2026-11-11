-- The task board of a chat: sprints, tasks, the status process and the journal.
-- Ids are per chat. Nothing is deleted physically: tasks and sprints are flagged, and the journal
-- is append-only.
CREATE TABLE board_sprints (
    chat_id    TEXT    NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    id         INTEGER NOT NULL,
    name       TEXT    NOT NULL,
    goal       TEXT    NOT NULL DEFAULT '',
    state      TEXT    NOT NULL,
    created_at TEXT    NOT NULL,
    started_at TEXT    NOT NULL DEFAULT '',
    closed_at  TEXT    NOT NULL DEFAULT '',
    deleted    INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (chat_id, id)
) WITHOUT ROWID;

-- At most one active sprint per chat, enforced by the database.
CREATE UNIQUE INDEX board_one_active_sprint ON board_sprints (chat_id) WHERE state = 'active' AND deleted = 0;

CREATE TABLE board_tasks (
    chat_id     TEXT    NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    id          INTEGER NOT NULL,
    title       TEXT    NOT NULL,
    description TEXT    NOT NULL DEFAULT '',
    status      TEXT    NOT NULL,
    sprint      INTEGER NOT NULL DEFAULT 0,
    assignee    TEXT    NOT NULL DEFAULT '',
    priority    INTEGER NOT NULL DEFAULT 0,
    created_by  TEXT    NOT NULL,
    created_at  TEXT    NOT NULL,
    updated_at  TEXT    NOT NULL,
    deleted     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (chat_id, id)
) WITHOUT ROWID;

CREATE INDEX board_tasks_by_sprint ON board_tasks (chat_id, sprint, deleted, status);
CREATE INDEX board_tasks_by_assignee ON board_tasks (chat_id, assignee, deleted);

CREATE TABLE board_workflow (
    chat_id    TEXT PRIMARY KEY REFERENCES chats (id) ON DELETE CASCADE,
    json       TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE board_log (
    chat_id     TEXT    NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    id          INTEGER NOT NULL,
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
    PRIMARY KEY (chat_id, id)
) WITHOUT ROWID;

CREATE INDEX board_log_by_entity ON board_log (chat_id, entity, entity_id, id);
