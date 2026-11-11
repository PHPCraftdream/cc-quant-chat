-- Nothing queries messages by sender or recipient alone (history, search and context go
-- through messages_by_thread and the primary key), so these indexes only slow down writes.
DROP INDEX IF EXISTS messages_by_from;
DROP INDEX IF EXISTS messages_by_to;
