-- Administrator operations (signed by the owner key), settings, and roles pinned to nicks.

-- A role the owner pinned to a nick: the member can't change or release it, and nobody else can
-- claim that role name while it is pinned.
ALTER TABLE nicks ADD COLUMN pinned_role TEXT NOT NULL DEFAULT '';

-- Settings changed through `ccqc config set`. scope is '' (broker-wide) or a chat id.
CREATE TABLE settings (
    scope TEXT NOT NULL,
    key   TEXT NOT NULL,
    value TEXT NOT NULL,
    PRIMARY KEY (scope, key)
) WITHOUT ROWID;

-- Every administrator attempt, refused ones included. args hold no secrets.
CREATE TABLE admin_log (
    id     INTEGER PRIMARY KEY AUTOINCREMENT,
    ts     TEXT NOT NULL,
    op     TEXT NOT NULL,
    args   TEXT NOT NULL,
    result TEXT NOT NULL,
    origin TEXT NOT NULL DEFAULT ''
);
