-- Authors sign their events (federation, stage 1). A nick is bound to an Ed25519 public key
-- (nicks.pubkey, base64; empty = a legacy nick that still joins by its secret). The signature
-- covers the chat id, the kind, the canonical JSON of the request and the chat's logical clock;
-- a row keeps the author's key, the signature and the signed payload (author_pub, sig, payload;
-- a comment shares the payload of its journal row), so each row can be checked on its own later.
ALTER TABLE nicks ADD COLUMN pubkey TEXT NOT NULL DEFAULT '';

ALTER TABLE messages ADD COLUMN author_pub TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN sig TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN payload TEXT NOT NULL DEFAULT '';

ALTER TABLE board_log ADD COLUMN author_pub TEXT NOT NULL DEFAULT '';
ALTER TABLE board_log ADD COLUMN sig TEXT NOT NULL DEFAULT '';
ALTER TABLE board_log ADD COLUMN payload TEXT NOT NULL DEFAULT '';

ALTER TABLE board_comments ADD COLUMN author_pub TEXT NOT NULL DEFAULT '';
ALTER TABLE board_comments ADD COLUMN sig TEXT NOT NULL DEFAULT '';

ALTER TABLE doc_versions ADD COLUMN author_pub TEXT NOT NULL DEFAULT '';
ALTER TABLE doc_versions ADD COLUMN sig TEXT NOT NULL DEFAULT '';
ALTER TABLE doc_versions ADD COLUMN payload TEXT NOT NULL DEFAULT '';
