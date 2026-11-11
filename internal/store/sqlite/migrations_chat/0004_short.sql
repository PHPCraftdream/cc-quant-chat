-- The short names of the origins of a chat (federation): the first four characters of the origin, or
-- more when another origin of the chat already has them. Given once, in the order the origins first
-- appear, so a member of another node is nick@<short> and two different nodes are never one.
CREATE TABLE origin_short (
    origin TEXT PRIMARY KEY,
    short  TEXT NOT NULL UNIQUE
) WITHOUT ROWID;
