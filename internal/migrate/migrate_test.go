package migrate_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	_ "modernc.org/sqlite"

	"github.com/PHPCraftdream/cc-quant-chat/internal/migrate"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "t.db"))+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func load(t *testing.T, files map[string]string) []migrate.Migration {
	t.Helper()
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys["m/"+name] = &fstest.MapFile{Data: []byte(body)}
	}
	ms, err := migrate.Load(fsys, "m")
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestLoadSortsByVersion(t *testing.T) {
	ms := load(t, map[string]string{
		"0002_b.sql": "CREATE TABLE b (x);",
		"0001_a.sql": "CREATE TABLE a (x);",
		"0010_c.sql": "CREATE TABLE c (x);",
	})
	var got []int
	for _, m := range ms {
		got = append(got, m.Version)
	}
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 10 {
		t.Fatalf("versions = %v, want [1 2 10]", got)
	}
}

func TestLoadRejectsBadNamesAndDuplicates(t *testing.T) {
	for name, files := range map[string]fstest.MapFS{
		"bad name":  {"m/init.sql": {Data: []byte("x")}},
		"zero":      {"m/0000_a.sql": {Data: []byte("x")}},
		"duplicate": {"m/0001_a.sql": {Data: []byte("x")}, "m/0001_b.sql": {Data: []byte("y")}},
	} {
		if _, err := migrate.Load(files, "m"); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestUpAppliesInOrderOnce(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	ms := load(t, map[string]string{
		"0001_a.sql": "CREATE TABLE a (x);",
		"0002_b.sql": "CREATE TABLE b (y REFERENCES a (x));",
	})
	applied, err := migrate.Up(ctx, db, ms)
	if err != nil || len(applied) != 2 {
		t.Fatalf("first Up: applied %d, err %v", len(applied), err)
	}
	applied, err = migrate.Up(ctx, db, ms)
	if err != nil || len(applied) != 0 {
		t.Fatalf("second Up: applied %d, err %v; want nothing", len(applied), err)
	}
	st, err := migrate.Status(ctx, db)
	if err != nil || len(st) != 2 || st[0].Version != 1 || st[1].Version != 2 {
		t.Fatalf("status = %+v, err %v", st, err)
	}
	if !tableExists(t, db, "a") || !tableExists(t, db, "b") {
		t.Fatal("tables a and b must exist")
	}
}

func TestEditedMigrationIsRejected(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	if _, err := migrate.Up(ctx, db, load(t, map[string]string{"0001_a.sql": "CREATE TABLE a (x);"})); err != nil {
		t.Fatal(err)
	}
	_, err := migrate.Up(ctx, db, load(t, map[string]string{"0001_a.sql": "CREATE TABLE a (x, y);"}))
	if err == nil || !strings.Contains(err.Error(), "changed after it was applied") {
		t.Fatalf("err = %v, want checksum error", err)
	}
}

func TestNewerDatabaseIsRejected(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	if _, err := migrate.Up(ctx, db, load(t, map[string]string{
		"0001_a.sql": "CREATE TABLE a (x);",
		"0002_b.sql": "CREATE TABLE b (x);",
	})); err != nil {
		t.Fatal(err)
	}
	_, err := migrate.Up(ctx, db, load(t, map[string]string{"0001_a.sql": "CREATE TABLE a (x);"}))
	if err == nil || !strings.Contains(err.Error(), "newer version") {
		t.Fatalf("err = %v, want newer-database error", err)
	}
}

func TestFailedMigrationLeavesNoTrace(t *testing.T) {
	ctx := context.Background()
	db := openDB(t)
	applied, err := migrate.Up(ctx, db, load(t, map[string]string{
		"0001_a.sql": "CREATE TABLE a (x);",
		"0002_b.sql": "CREATE TABLE b (x); THIS IS NOT SQL;",
	}))
	if err == nil {
		t.Fatal("want error from broken migration")
	}
	if len(applied) != 1 || applied[0].Version != 1 {
		t.Fatalf("applied = %+v, want only 0001", applied)
	}
	if tableExists(t, db, "b") {
		t.Fatal("table b from the failed migration must be rolled back")
	}
	st, err := migrate.Status(ctx, db)
	if err != nil || len(st) != 1 {
		t.Fatalf("status = %+v, err %v; want only 0001 recorded", st, err)
	}
}

func TestChecksumIgnoresLineEndings(t *testing.T) {
	lf := load(t, map[string]string{"0001_a.sql": "-- a\nCREATE TABLE a (x);\n"})[0]
	crlf := load(t, map[string]string{"0001_a.sql": "-- a\r\nCREATE TABLE a (x);\r\n"})[0]
	if lf.Sum != crlf.Sum {
		t.Fatalf("sums differ: %s vs %s", lf.Sum, crlf.Sum)
	}
	if strings.Contains(crlf.SQL, "\r") {
		t.Fatal("SQL kept CR characters")
	}
}

func TestDatabaseWithRawCRLFChecksumStillPasses(t *testing.T) {
	// A database migrated by a build that hashed a CRLF checkout must still be accepted.
	raw := "CREATE TABLE a (x);\r\n"
	sum := sha256.Sum256([]byte(raw))
	ms := load(t, map[string]string{"0001_a.sql": "CREATE TABLE a (x);\n"})
	applied := []migrate.Applied{{Version: 1, Name: "a", Sum: hex.EncodeToString(sum[:])}}
	pending, err := migrate.Check(applied, ms)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending %v, err %v; want accepted", pending, err)
	}
}

func TestStatusDoesNotWrite(t *testing.T) {
	db := openDB(t)
	applied, err := migrate.Status(context.Background(), db)
	if err != nil || len(applied) != 0 {
		t.Fatalf("applied %v, err %v", applied, err)
	}
	if tableExists(t, db, "schema_migrations") {
		t.Fatal("Status created schema_migrations")
	}
}

const rebuild = `-- ccqc:foreign-keys=off
CREATE TABLE parent_new (id INTEGER PRIMARY KEY, extra TEXT NOT NULL DEFAULT '');
INSERT INTO parent_new (id) SELECT id FROM parent;
DROP TABLE parent;
ALTER TABLE parent_new RENAME TO parent;
`

func TestForeignKeysOffMigrationKeepsChildren(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	base := map[string]string{"0001_a.sql": `CREATE TABLE parent (id INTEGER PRIMARY KEY);
CREATE TABLE child (id INTEGER PRIMARY KEY, pid INTEGER NOT NULL REFERENCES parent (id) ON DELETE CASCADE);
INSERT INTO parent (id) VALUES (1);
INSERT INTO child (id, pid) VALUES (10, 1);`}
	if _, err := migrate.Up(ctx, db, load(t, base)); err != nil {
		t.Fatal(err)
	}
	base["0002_rebuild.sql"] = rebuild
	if _, err := migrate.Up(ctx, db, load(t, base)); err != nil {
		t.Fatal(err)
	}
	var children int
	if err := db.QueryRow(`SELECT count(*) FROM child`).Scan(&children); err != nil || children != 1 {
		t.Fatalf("children = %d, err %v; the rebuild must not cascade-delete them", children, err)
	}
	var fk int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d, err %v; must be switched back on", fk, err)
	}
}

func TestForeignKeysOffMigrationFailsOnBrokenReferences(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	base := map[string]string{"0001_a.sql": `CREATE TABLE parent (id INTEGER PRIMARY KEY);
CREATE TABLE child (id INTEGER PRIMARY KEY, pid INTEGER NOT NULL REFERENCES parent (id));
INSERT INTO parent (id) VALUES (1);
INSERT INTO child (id, pid) VALUES (10, 1);`}
	if _, err := migrate.Up(ctx, db, load(t, base)); err != nil {
		t.Fatal(err)
	}
	base["0002_bad.sql"] = "-- ccqc:foreign-keys=off\nDELETE FROM parent;\n"
	if _, err := migrate.Up(ctx, db, load(t, base)); err == nil || !strings.Contains(err.Error(), "foreign_key_check") {
		t.Fatalf("err = %v; want foreign_key_check failure", err)
	}
	applied, _ := migrate.Status(ctx, db)
	if len(applied) != 1 {
		t.Fatalf("applied = %+v; the broken migration must leave no trace", applied)
	}
}
