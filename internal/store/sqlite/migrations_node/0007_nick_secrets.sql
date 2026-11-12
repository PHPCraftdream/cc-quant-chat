-- Nick secrets (docs/design-keys.md): for a nick whose key is protected, the broker's half of it.
--
-- b: 32 random bytes, the broker's secret for this (chat, nick); the nick's signing key is derived
-- from it and from the client's secret A, which the broker never has. auth_pub: the public half of
-- the key derived from A alone (domain "auth"), with which a client proves it holds A before b is given
-- out. sign_pub: the public key registered for the nick in the chat (base64), to check that the two
-- databases agree. sid_hash: SHA-256 of the Claude Code session id the nick is bound to ('' = a
-- terminal nick, no binding). fails / fail_until: wrong proofs in a row and the time (unix seconds)
-- before which no new proof is taken.
--
-- state: 'pending' is written before the nick's key is registered in the chat database, 'active'
-- after; a pending row left by a crash is settled when the broker starts (the chat's key equals
-- sign_pub: active, else dropped). A pending row and an active one of one nick may coexist (a key
-- that is being replaced). Lives in node.db only: never in a chat database, never replicated.
CREATE TABLE nick_secrets (
    chat_id     TEXT    NOT NULL,
    nick        TEXT    NOT NULL,
    state       TEXT    NOT NULL CHECK (state IN ('pending', 'active')),
    b           BLOB    NOT NULL,
    auth_pub    BLOB    NOT NULL,
    sign_pub    TEXT    NOT NULL,
    sid_hash    TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    unlocked_at INTEGER NOT NULL DEFAULT 0,
    fails       INTEGER NOT NULL DEFAULT 0,
    fail_until  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (chat_id, nick, state)
) WITHOUT ROWID;
