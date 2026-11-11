-- The journal of a chat (federation, stage 2): one row per change, chained per (chat, origin) and
-- signed by the node that accepted it. Everything else (messages, board_*, docs, doc_versions,
-- nicks, rooms, room_members, chat settings) is a projection of these rows and can be rebuilt
-- from them (`ccqc db rebuild`); `ccqc db verify` checks the chains and the signatures.
--
-- payload: what the author sent and signed (canonical JSON). assigned: what the node issued or
-- wrote (ids, row images); covered by node_sig. prev: ID of the previous event of the same
-- (chat, origin), see internal/events. Rows written before this migration are moved here by the
-- broker at its first start (see meta); SQL cannot sign with the node key.
CREATE TABLE events (
    chat_id    TEXT    NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
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
    PRIMARY KEY (chat_id, origin, seq)
) WITHOUT ROWID;

-- Small facts about the database itself, e.g. which chats have been moved into events.
CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) WITHOUT ROWID;
