package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/migrate"
)

func TestNodesListedBeforeLevelsExistedStayTrustedAndActive(t *testing.T) {
	raw, err := OpenRaw(filepath.Join(t.TempDir(), "node.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	ms, err := NodeMigrations()
	if err != nil || len(ms) < 3 {
		t.Fatalf("%d migrations, %v", len(ms), err)
	}
	// The node.db of stage 3: a node added by hand, and one with a value that is no level.
	if _, err := migrate.Up(context.Background(), raw, ms[:2]); err != nil {
		t.Fatal(err)
	}
	a, b := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for _, p := range [][2]string{{a, "trusted"}, {b, "whatever"}} {
		if _, err := raw.Exec(`INSERT INTO peers (origin, addr, trust, added_at) VALUES (?, 'tcp://127.0.0.1:1', ?, '2026-01-01T00:00:00Z')`, p[0], p[1]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := migrate.Up(context.Background(), raw, ms); err != nil {
		t.Fatal(err)
	}
	for _, o := range []string{a, b} {
		var trust, state, until string
		if err := raw.QueryRow(`SELECT trust, state, pending_until FROM peers WHERE origin = ?`, o).Scan(&trust, &state, &until); err != nil {
			t.Fatal(err)
		}
		if trust != "trusted" || state != "active" || until != "" {
			t.Errorf("%.4s after the migration: %q %q %q", o, trust, state, until)
		}
	}
}

func TestALevelIsOneOfThreeAndOnlyOfANodeThatIsListed(t *testing.T) {
	db, _ := openTemp(t)
	o := strings.Repeat("c", 32)
	if err := db.AddPeer(o, ""); err != nil {
		t.Fatal(err)
	}
	if old, err := db.SetPeerTrust(o, "member"); err != nil || old != "trusted" {
		t.Fatalf("%q %v", old, err)
	}
	if _, err := db.SetPeerTrust(o, "admin"); err == nil {
		t.Error("a level that is none of read, member, trusted was taken")
	}
	if _, err := db.SetPeerTrust(strings.Repeat("d", 32), "read"); err == nil {
		t.Error("the level of a node that is not listed was set")
	}
	ps, err := db.Peers()
	if err != nil || len(ps) != 1 || ps[0].Trust != "member" || ps[0].State != "active" {
		t.Fatalf("%+v %v", ps, err)
	}
}
