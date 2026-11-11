-- Idempotent send: a sender's retry carries the same client_msg_id and must not store a second
-- message. The unique index covers non-empty ids only.
ALTER TABLE messages ADD COLUMN client_msg_id TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX messages_by_client_id ON messages (chat_id, from_nick, client_msg_id) WHERE client_msg_id <> '';
