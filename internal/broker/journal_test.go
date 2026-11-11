package broker

import (
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
	"github.com/PHPCraftdream/cc-quant-chat/internal/node"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store/sqlite"
)

// journalBroker is a broker on a database whose events are signed by a node key, as in life.
func journalBroker(t *testing.T) (*Broker, *sqlite.DB, string, ed25519.PrivateKey) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ccqc.db")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	origin := node.Origin(pub)
	if err := db.RegisterOrigin(origin, pub); err != nil {
		t.Fatal(err)
	}
	db.SetNode(origin, priv)
	b := newTestBroker(db)
	b.origin = origin
	return b, db, path, priv
}

// dump is every projection row of the database file, as text, without the read cursors.
func dump(t *testing.T, path string) string {
	t.Helper()
	raw, err := sqlite.OpenRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var out strings.Builder
	for _, q := range []string{
		"SELECT * FROM rooms ORDER BY 1", "SELECT * FROM room_members ORDER BY 1, 2",
		"SELECT nick, key_hash, pinned_role, pubkey FROM nicks ORDER BY 1",
		"SELECT * FROM messages ORDER BY 1", "SELECT * FROM board_sprints ORDER BY 1", "SELECT * FROM board_tasks ORDER BY 1",
		"SELECT * FROM board_workflow ORDER BY 1", "SELECT * FROM board_log ORDER BY 1", "SELECT * FROM board_comments ORDER BY 1",
		"SELECT * FROM docs ORDER BY 1", "SELECT * FROM doc_versions ORDER BY 1, 2", "SELECT * FROM settings ORDER BY 1",
		"SELECT lamport FROM chat",
	} {
		rows, err := raw.Query(q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&out, "%.12s:%v\n", q[7:], vals)
		}
		rows.Close()
	}
	return out.String()
}

// consistent checks that the journal verifies and that the projections are what it gives.
func consistent(t *testing.T, db *sqlite.DB, after string) {
	t.Helper()
	ids, err := db.ChatIDs()
	if err != nil || len(ids) == 0 {
		t.Fatalf("after %s: chats: %v %v", after, ids, err)
	}
	for _, id := range ids {
		rep, err := db.Rebuild(id, true)
		if err != nil {
			t.Fatalf("after %s: rebuild: %v", after, err)
		}
		if rep.Total() != 0 {
			t.Fatalf("after %s: the projections differ from the journal: %v", after, rep.Diffs)
		}
		v, err := db.Verify(id)
		if err != nil || len(v.Issues) > 0 {
			t.Fatalf("after %s: verify: %v %v", after, err, v.Issues)
		}
	}
}

// chatFile is the database file of a chat in the data directory of the broker whose old database
// file is path.
func chatFile(path, id string) string { return filepath.Join(filepath.Dir(path), "chats", id+".db") }

func mustCode(t *testing.T, what, code string) {
	t.Helper()
	if code != "" {
		t.Fatalf("%s: %q", what, code)
	}
}

// runScenario does one of everything a chat does, checking after each step that the event and the
// projections agree. It returns the database path, the chat id and the node key.
func runScenario(t *testing.T) (*sqlite.DB, string, string, ed25519.PrivateKey) {
	t.Helper()
	b, db, path, priv := journalBroker(t)
	tok, id := token(t)
	alice := b.mustJoin(t, tok, "alice", newKey(t)) // signs everything
	consistent(t, db, "alice joins with a key")
	bob := b.connect(t) // a client of the previous level: no key, no signatures
	if _, code := bob.join(tok, "bob", proto.LegacyProto); code != "" {
		t.Fatalf("bob joins: %q", code)
	}
	consistent(t, db, "bob joins by his secret")

	mustCode(t, "room_create", alice.call("room_create", proto.RoomArgs{Room: "ops", Topic: "operations"}, nil))
	mustCode(t, "room_join", bob.call("room_join", proto.RoomArgs{Room: "ops"}, nil))
	consistent(t, db, "a room and its members")
	mustCode(t, "room_leave", bob.call("room_leave", proto.RoomArgs{Room: "ops"}, nil))
	consistent(t, db, "a member leaves")

	if _, err := alice.do("send", "msg", &proto.SendArgs{Room: "common", Text: "signed <hello> & more", ClientMsgID: "c1"}, nil); err != nil {
		t.Fatal(err)
	}
	consistent(t, db, "a signed message")
	if _, code := bob.sendID("common", "", "unsigned from bob", "b1"); code != "" {
		t.Fatalf("bob's message: %q", code)
	}
	if _, code := bob.send("direct", "alice", "a private word"); code != "" {
		t.Fatalf("bob's direct message: %q", code)
	}
	consistent(t, db, "unsigned and direct messages")

	steps := []struct {
		what string
		a    proto.BoardArgs
		by   *signedPeer // nil: bob, who signs nothing
	}{
		{"sprint.create", proto.BoardArgs{Op: "sprint.create", Name: sp("one"), Goal: sp("ship")}, alice},
		{"sprint.start", proto.BoardArgs{Op: "sprint.start", ID: 1}, nil},
		{"task.create 1", proto.BoardArgs{Op: "task.create", Title: sp("first"), Description: sp("long <text>"), Sprint: func() *int64 { v := int64(1); return &v }()}, alice},
		{"task.create 2", proto.BoardArgs{Op: "task.create", Title: sp("second")}, nil},
		{"task.update", proto.BoardArgs{Op: "task.update", ID: 1, Title: sp("first, renamed")}, alice},
		{"task.move", proto.BoardArgs{Op: "task.move", ID: 1, Status: "doing"}, nil},
		{"board.revert", proto.BoardArgs{Op: "board.revert", LogID: 6}, alice},
		{"task.comment", proto.BoardArgs{Op: "task.comment", ID: 1, Text: "on it"}, alice},
		{"sprint.close", proto.BoardArgs{Op: "sprint.close", ID: 1, Carry: 0}, nil},
		{"board.workflow_set", proto.BoardArgs{Op: "board.workflow_set", Workflow: []byte(`{"statuses":["todo","doing","done"],"initial":"todo","terminal":["done"],"transitions":{"todo":["doing"],"doing":["done"]}}`)}, alice},
		{"task.delete", proto.BoardArgs{Op: "task.delete", ID: 2}, nil},
	}
	for _, s := range steps {
		var err error
		var code string
		if s.by != nil {
			_, err = s.by.do("board", s.a.Op, &s.a, nil)
			code = codeOf(err)
		} else {
			_, code = bob.board(s.a)
		}
		mustCode(t, s.what, code)
		consistent(t, db, s.what)
	}

	docSteps := []struct {
		what string
		a    proto.DocArgs
		by   *signedPeer
	}{
		{"put", proto.DocArgs{Op: "put", Path: "/a", Body: "# A\nbody <1>"}, alice},
		{"append", proto.DocArgs{Op: "append", Path: "/a", Text: "more"}, nil},
		{"move", proto.DocArgs{Op: "move", Path: "/a", To: "/b"}, alice},
		{"delete", proto.DocArgs{Op: "delete", Path: "/b"}, nil},
		{"revert", proto.DocArgs{Op: "revert", Path: "/b", Version: 1}, alice},
	}
	for _, s := range docSteps {
		var code string
		if s.by != nil {
			_, err := s.by.do("doc", "doc."+s.a.Op, &s.a, nil)
			code = codeOf(err)
		} else {
			_, code = bob.doc(s.a)
		}
		mustCode(t, "doc."+s.what, code)
		consistent(t, db, "doc."+s.what)
	}

	if err := db.SetSetting(id, "board.manager_role", "lead"); err != nil {
		t.Fatal(err)
	}
	consistent(t, db, "a chat setting")
	bob.call("leave", nil, nil)
	if _, perr := b.nickReset(proto.NickResetArgs{Token: tok, Name: "bob"}); perr != nil {
		t.Fatalf("nick reset: %v", perr)
	}
	consistent(t, db, "a nick reset")
	return db, chatFile(path, id), id, priv
}

func TestEveryChangeIsAnEventAndTheProjectionsAreWhatTheJournalGives(t *testing.T) {
	db, path, id, _ := runScenario(t)
	before := dump(t, path)

	// A projection that was damaged (a message lost, a task edited, a page gone) is rebuilt from
	// the journal into exactly what it was; the read cursors are not touched.
	raw, err := sqlite.OpenRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"DELETE FROM messages WHERE id = 1", "UPDATE board_tasks SET title = 'tampered' WHERE id = 1",
		"DELETE FROM docs", "UPDATE nicks SET pinned_role = 'x'", "DELETE FROM board_comments",
		"UPDATE nicks SET seen = 7",
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	raw.Close()
	if dump(t, path) == before {
		t.Fatal("the damage was not done")
	}
	rep, err := db.Rebuild(id, false)
	if err != nil || !rep.Replaced || rep.Total() == 0 {
		t.Fatalf("rebuild: %+v, %v", rep, err)
	}
	for _, table := range []string{"messages", "board_tasks", "docs", "nicks", "board_comments"} {
		if rep.Diffs[table] == 0 {
			t.Errorf("no difference found in %s: %v", table, rep.Diffs)
		}
	}
	if got := dump(t, path); got != before {
		t.Fatalf("the rebuilt projections are not the original ones:\n%s\n--- want ---\n%s", got, before)
	}
	raw, _ = sqlite.OpenRaw(path)
	defer raw.Close()
	var seen int64
	if err := raw.QueryRow("SELECT MAX(seen) FROM nicks").Scan(&seen); err != nil || seen != 7 {
		t.Fatalf("the read cursors must survive a rebuild: %d, %v", seen, err)
	}
	if rep, err := db.Rebuild(id, false); err != nil || rep.Total() != 0 || rep.Replaced {
		t.Fatalf("a second rebuild finds %+v, %v", rep, err)
	}
}

// signedSeq: the seq of the first event that carries an author's signature.
func signedSeq(t *testing.T, raw *sql.DB) int64 {
	t.Helper()
	var seq int64
	if err := raw.QueryRow("SELECT seq FROM events WHERE sig != '' ORDER BY seq LIMIT 1").Scan(&seq); err != nil {
		t.Fatal(err)
	}
	return seq
}

func issuesWith(rep sqlite.VerifyReport, part string) bool {
	for _, i := range rep.Issues {
		if strings.Contains(i.What, part) {
			return true
		}
	}
	return false
}

func TestVerifyFindsAForgedPayloadABrokenChainAndAForeignNodeSignature(t *testing.T) {
	db, path, id, priv := runScenario(t)
	raw, err := sqlite.OpenRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := raw.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	verify := func() sqlite.VerifyReport {
		t.Helper()
		rep, err := db.Verify(id)
		if err != nil {
			t.Fatal(err)
		}
		return rep
	}
	if rep := verify(); len(rep.Issues) != 0 || rep.Signed == 0 || rep.Events < 20 {
		t.Fatalf("the untouched journal: %+v", rep)
	}
	var origin string
	if err := raw.QueryRow("SELECT origin FROM events LIMIT 1").Scan(&origin); err != nil {
		t.Fatal(err)
	}
	read := func(seq int64) events.Event {
		t.Helper()
		e := events.Event{ChatID: id, Origin: origin, Seq: seq}
		if err := raw.QueryRow(`SELECT prev, lamport, kind, payload, author_pub, sig, assigned, ts, node_sig FROM events WHERE origin = ? AND seq = ?`,
			origin, seq).Scan(&e.Prev, &e.Lamport, &e.Kind, &e.Payload, &e.AuthorPub, &e.Sig, &e.Assigned, &e.TS, &e.NodeSig); err != nil {
			t.Fatal(err)
		}
		return e
	}
	seq := signedSeq(t, raw)
	orig := read(seq)

	// 1. The payload is changed: the node's signature no longer covers it.
	exec("UPDATE events SET payload = ? WHERE seq = ?", `{"room":"common","text":"forged"}`, seq)
	if rep := verify(); !issuesWith(rep, "node's signature does not verify") {
		t.Fatalf("a forged payload: %v", rep.Issues)
	}
	// 2. ... and re-signed by the node, the author's signature still gives it away.
	forged := read(seq)
	forged.Sign(priv)
	exec("UPDATE events SET node_sig = ? WHERE seq = ?", forged.NodeSig, seq)
	if rep := verify(); !issuesWith(rep, "author's signature does not verify") {
		t.Fatalf("a payload re-signed by the node: %v", rep.Issues)
	}
	exec("UPDATE events SET payload = ?, node_sig = ? WHERE seq = ?", orig.Payload, orig.NodeSig, seq)
	if rep := verify(); len(rep.Issues) != 0 {
		t.Fatalf("restored: %v", rep.Issues)
	}

	// 3. The node's signature is made by another key.
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	foreign := orig
	foreign.Sign(other)
	exec("UPDATE events SET node_sig = ? WHERE seq = ?", foreign.NodeSig, seq)
	if rep := verify(); !issuesWith(rep, "node's signature does not verify") {
		t.Fatalf("a foreign node signature: %v", rep.Issues)
	}
	exec("UPDATE events SET node_sig = ? WHERE seq = ?", orig.NodeSig, seq)

	// 4. The chain is cut: an event removed, then a link changed.
	gone := read(seq + 1)
	exec("DELETE FROM events WHERE seq = ?", seq+1)
	if rep := verify(); !issuesWith(rep, "gap") {
		t.Fatalf("a removed event: %v", rep.Issues)
	}
	exec(`INSERT INTO events (origin, seq, prev, lamport, kind, payload, author_pub, sig, assigned, ts, node_sig) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		origin, gone.Seq, gone.Prev, gone.Lamport, gone.Kind, gone.Payload, gone.AuthorPub, gone.Sig, gone.Assigned, gone.TS, gone.NodeSig)
	exec("UPDATE events SET prev = 'ab' WHERE seq = ?", seq+1)
	if rep := verify(); !issuesWith(rep, "chain is broken") {
		t.Fatalf("a changed link: %v", rep.Issues)
	}
	exec("UPDATE events SET prev = ? WHERE seq = ?", gone.Prev, seq+1)
	if rep := verify(); len(rep.Issues) != 0 {
		t.Fatalf("restored: %v", rep.Issues)
	}
}

func TestTheJournalAndItsSignaturesSurviveARestart(t *testing.T) {
	_, path, id, priv := runScenario(t)
	db := reopen(t, filepath.Join(filepath.Dir(filepath.Dir(path)), "ccqc.db")) // path is the chat's file
	pub := priv.Public().(ed25519.PublicKey)
	db.SetNode(node.Origin(pub), priv)
	rep, err := db.Verify(id)
	if err != nil || len(rep.Issues) != 0 || rep.Events == 0 {
		t.Fatalf("after a restart: %+v, %v", rep, err)
	}
	if rep, err := db.Rebuild(id, true); err != nil || rep.Total() != 0 {
		t.Fatalf("rebuild after a restart: %+v, %v", rep, err)
	}
}

// A failed write of the event takes the projection with it: nothing is half-done.
func TestAnEventThatCannotBeWrittenRollsBackItsProjection(t *testing.T) {
	b, _, path, _ := journalBroker(t)
	tok, id := token(t)
	a := b.mustJoin(t, tok, "alice", newKey(t))
	raw, err := sqlite.OpenRaw(chatFile(path, id))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`CREATE TRIGGER journal_full BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT, 'journal is full'); END`); err != nil {
		t.Fatal(err)
	}
	count := func(q string) int {
		var n int
		if err := raw.QueryRow(q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if _, err := a.do("send", "msg", &proto.SendArgs{Room: "common", Text: "lost", ClientMsgID: "x1"}, nil); codeOf(err) != "store_failed" {
		t.Fatalf("send: %v", err)
	}
	if _, err := a.do("board", "task.create", &proto.BoardArgs{Op: "task.create", Title: sp("lost")}, nil); codeOf(err) != "store_failed" {
		t.Fatalf("board: %v", err)
	}
	if _, err := a.do("doc", "doc.put", &proto.DocArgs{Op: "put", Path: "/x", Body: "lost"}, nil); codeOf(err) != "store_failed" {
		t.Fatalf("doc: %v", err)
	}
	for _, q := range []string{
		"SELECT COUNT(*) FROM messages", "SELECT COUNT(*) FROM board_tasks",
		"SELECT COUNT(*) FROM board_log", "SELECT COUNT(*) FROM doc_versions",
		"SELECT lamport FROM chat",
	} {
		if n := count(q); n != 0 {
			t.Errorf("%s = %d: a projection was written without its event", q, n)
		}
	}
	if _, err := raw.Exec(`DROP TRIGGER journal_full`); err != nil {
		t.Fatal(err)
	}
	if _, err := a.do("send", "msg", &proto.SendArgs{Room: "common", Text: "found"}, nil); err != nil {
		t.Fatalf("after the journal works again: %v", err)
	}
}

// The events of administrator operations (a pinned role, a chat setting, a freed nick) keep the
// owner's signature, so the journal shows on its own who decided it.
func TestAdministratorEventsCarryTheOwnersSignatureAndVerifyChecksIt(t *testing.T) {
	b, db, path, priv := journalBroker(t)
	tok, id := token(t)
	k := newKey(t)
	register(t, b, k)
	b.mustJoin(t, tok, "alice", newKey(t))
	bob := b.connect(t)
	if _, code := bob.join(tok, "bob", proto.LegacyProto); code != "" {
		t.Fatalf("bob: %q", code)
	}
	bob.call("leave", nil, nil)
	c := b.connect(t)
	mustCode(t, "role_give", adminReq(t, c, k, "role_give", proto.AdminRole{Chat: id, Nick: "alice", Role: "lead"}))
	mustCode(t, "config_set", adminReq(t, c, k, "config_set", proto.AdminConfig{Chat: id, Key: "board.manager_role", Value: " lead "}))
	mustCode(t, "nick_reset", adminReq(t, c, k, "nick_reset", proto.AdminRole{Chat: id, Nick: "bob"}))
	consistent(t, db, "three administrator operations")
	rep, err := db.Verify(id)
	if err != nil || rep.Signed != 3 {
		t.Fatalf("%d signed events (want the three operations): %+v %v", rep.Signed, rep.Issues, err)
	}

	raw, err := sqlite.OpenRaw(chatFile(path, id))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var origin string
	var seq int64
	if err := raw.QueryRow("SELECT origin, seq FROM events WHERE kind = 'nick.pin'").Scan(&origin, &seq); err != nil {
		t.Fatal(err)
	}
	e := events.Event{ChatID: id, Origin: origin, Seq: seq}
	if err := raw.QueryRow(`SELECT prev, lamport, kind, payload, author_pub, sig, assigned, ts FROM events WHERE seq = ?`, seq).
		Scan(&e.Prev, &e.Lamport, &e.Kind, &e.Payload, &e.AuthorPub, &e.Sig, &e.Assigned, &e.TS); err != nil {
		t.Fatal(err)
	}
	// The node cannot make up a decision of the owner: with another role in the payload (and the
	// node's own signature renewed) the owner's signature no longer matches.
	e.Payload = strings.Replace(e.Payload, `"lead"`, `"boss"`, 1)
	e.Sign(priv)
	if _, err := raw.Exec("UPDATE events SET payload = ?, node_sig = ? WHERE seq = ?", e.Payload, e.NodeSig, seq); err != nil {
		t.Fatal(err)
	}
	if rep, err := db.Verify(id); err != nil || !issuesWith(rep, "author's signature does not verify") {
		t.Fatalf("a changed decision: %v %v", rep.Issues, err)
	}
}

// The event of a board change says, field by field, what the change did, next to the row images:
// a receiver can check "task.move changes only the status" without running the rules again.
func TestBoardEventsListTheFieldsTheyChanged(t *testing.T) {
	_, path, _, _ := runScenario(t)
	raw, err := sqlite.OpenRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var assigned string
	if err := raw.QueryRow("SELECT assigned FROM events WHERE kind = 'task.move'").Scan(&assigned); err != nil {
		t.Fatal(err)
	}
	var a struct {
		Changes []struct {
			Entity string                       `json:"entity"`
			ID     int64                        `json:"entity_id"`
			Fields map[string]map[string]string `json:"fields"`
		} `json:"changes"`
	}
	if err := json.Unmarshal([]byte(assigned), &a); err != nil || len(a.Changes) != 1 {
		t.Fatalf("changes: %s (%v)", assigned, err)
	}
	c := a.Changes[0]
	if c.Entity != "task" || c.ID != 1 || len(c.Fields) != 1 || c.Fields["status"]["before"] != "todo" || c.Fields["status"]["after"] != "doing" {
		t.Fatalf("task.move changes %+v, want only status todo → doing", c)
	}
}
