-- The owner: the human who owns the broker on this machine. One row, the Ed25519 public key; the
-- private key never reaches the broker. Messages sent over an owner-verified connection are
-- marked, and the mark is part of the history.
CREATE TABLE owner (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    pubkey     BLOB NOT NULL,
    created_at TEXT NOT NULL
);

ALTER TABLE messages ADD COLUMN owner INTEGER NOT NULL DEFAULT 0;
