package docs

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store/sqlite"
)

const chatA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const chatB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const origin = "00112233445566778899aabbccddeeff"

type env struct {
	t    *testing.T
	db   *sqlite.DB
	path string
	now  time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ccqc.db")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, id := range []string{chatA, chatB} {
		if _, err := db.Load(id, "team"); err != nil {
			t.Fatal(err)
		}
	}
	return &env{t: t, db: db, path: path, now: time.Date(2026, 11, 11, 14, 0, 0, 0, time.UTC)}
}

func (e *env) do(actor string, fn func(c Ctx) (Result, error)) (Result, error) {
	e.t.Helper()
	e.now = e.now.Add(time.Minute)
	var r Result
	err := e.db.DoDocs(chatA, func(tx store.DocsTx) error {
		var err error
		r, err = fn(Ctx{Tx: tx, Actor: actor, Origin: origin, Now: e.now})
		return err
	})
	return r, err
}

func (e *env) must(actor string, fn func(c Ctx) (Result, error)) Result {
	e.t.Helper()
	r, err := e.do(actor, fn)
	if err != nil {
		e.t.Fatalf("%v", err)
	}
	return r
}

func (e *env) view(chat string, fn func(tx store.DocsTx) error) {
	e.t.Helper()
	if err := e.db.ViewDocs(chat, fn); err != nil {
		e.t.Fatal(err)
	}
}

func code(err error) string {
	var pe *proto.Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	if err == nil {
		return ""
	}
	return "other: " + err.Error()
}

func put(path, body string, base *int64) func(c Ctx) (Result, error) {
	return func(c Ctx) (Result, error) { return Put(c, path, nil, body, base, "") }
}

func v(n int64) *int64 { return &n }

func (e *env) get(chat, path, section string, version int64, full bool) (string, string) {
	e.t.Helper()
	var text, body string
	e.view(chat, func(tx store.DocsTx) error {
		var err error
		text, body, _, err = Get(tx, path, section, version, full)
		return err
	})
	return text, body
}

func TestWritesMakeVersionsAndHistoryKeepsThem(t *testing.T) {
	e := newEnv(t)
	r := e.must("alice", put("/arch/broker", "# Broker\nfirst", nil))
	if r.Version != 1 || !strings.Contains(r.Text, "created v1") {
		t.Fatalf("create: %+v", r)
	}
	r = e.must("bob", put("/arch/broker", "# Broker\nsecond", v(1)))
	if r.Version != 2 {
		t.Fatalf("replace: %+v", r)
	}
	r = e.must("alice", func(c Ctx) (Result, error) { return Append(c, "/arch/broker", "third line", v(2)) })
	if r.Version != 3 {
		t.Fatalf("append: %+v", r)
	}
	_, body := e.get(chatA, "/arch/broker", "", 0, false)
	if body != "# Broker\nsecond\nthird line" {
		t.Fatalf("current body %q", body)
	}
	if _, old := e.get(chatA, "/arch/broker", "", 1, false); old != "# Broker\nfirst" {
		t.Fatalf("v1 body %q", old)
	}
	var hist string
	e.view(chatA, func(tx store.DocsTx) error { var err error; hist, err = History(tx, "/arch/broker", 0, 0); return err })
	if !strings.Contains(hist, "v3") || !strings.Contains(hist, "@bob") || !strings.Contains(hist, "append") || strings.Index(hist, "v3") > strings.Index(hist, "v1") {
		t.Fatalf("history newest first:\n%s", hist)
	}
	// An identical put writes no version.
	if r := e.must("alice", put("/arch/broker", "# Broker\nsecond\nthird line", v(3))); r.Changed || r.Version != 3 {
		t.Fatalf("unchanged put: %+v", r)
	}
}

func TestBaseDecidesWhoMayReplace(t *testing.T) {
	e := newEnv(t)
	e.must("alice", put("/p", "one", nil))
	e.must("bob", put("/p", "two", v(1)))
	_, err := e.do("alice", put("/p", "mine", v(1)))
	if code(err) != "conflict" || !strings.Contains(err.Error(), "v2") || !strings.Contains(err.Error(), "@bob") {
		t.Fatalf("stale base: %v; want a conflict naming v2 and bob", err)
	}
	if _, err := e.do("alice", put("/p", "no base", nil)); code(err) != "base_required" {
		t.Fatalf("replace without base: %v", err)
	}
	if _, err := e.do("alice", put("/new", "x", v(4))); code(err) != "conflict" {
		t.Fatalf("a base for a page that does not exist: %v", err)
	}
	if _, err := e.do("alice", func(c Ctx) (Result, error) { return Append(c, "/p", "x", v(1)) }); code(err) != "conflict" {
		t.Fatalf("append with a stale base: %v", err)
	}
	if _, err := e.do("alice", func(c Ctx) (Result, error) { return Append(c, "/nope", "x", nil) }); code(err) != "no_such_page" {
		t.Fatalf("append to a missing page: %v", err)
	}
	// Nothing above changed the page.
	if _, body := e.get(chatA, "/p", "", 0, false); body != "two" {
		t.Fatalf("body %q", body)
	}
}

func TestRevertDeleteAndMoveAreReversibleThroughHistory(t *testing.T) {
	e := newEnv(t)
	e.must("a", put("/x/page", "v1 text", nil))
	e.must("a", put("/x/page", "v2 text", v(1)))
	r := e.must("a", func(c Ctx) (Result, error) { return Revert(c, "/x/page", 1) }) // v3 = content of v1
	if r.Version != 3 {
		t.Fatalf("revert: %+v", r)
	}
	if _, body := e.get(chatA, "/x/page", "", 0, false); body != "v1 text" {
		t.Fatalf("after the revert: %q", body)
	}
	e.must("a", func(c Ctx) (Result, error) { return Revert(c, "/x/page", 2) }) // revert of the revert: v4 = v2's content
	if _, body := e.get(chatA, "/x/page", "", 0, false); body != "v2 text" {
		t.Fatalf("revert of the revert: %q", body)
	}

	e.must("a", func(c Ctx) (Result, error) { return Delete(c, "/x/page") }) // v5, deleted
	var tree string
	e.view(chatA, func(tx store.DocsTx) error { var err error; tree, _, err = Tree(tx, "", 0); return err })
	if strings.Contains(tree, "/x/page") {
		t.Fatalf("a deleted page is still listed:\n%s", tree)
	}
	if _, err := e.do("a", func(c Ctx) (Result, error) { return Delete(c, "/x/page") }); code(err) != "no_such_page" {
		t.Fatalf("deleting twice: %v", err)
	}
	e.must("a", func(c Ctx) (Result, error) { return Revert(c, "/x/page", 4) }) // back
	if _, body := e.get(chatA, "/x/page", "", 0, false); body != "v2 text" {
		t.Fatalf("after reverting the delete: %q", body)
	}
	// A deleted version can itself be restored by reverting to it: the page is deleted again.
	e.must("a", func(c Ctx) (Result, error) { return Revert(c, "/x/page", 5) })
	if _, err := e.do("a", func(c Ctx) (Result, error) { _, err := Append(c, "/x/page", "z", nil); return Result{}, err }); code(err) != "no_such_page" {
		t.Fatalf("page after reverting to a deleted version: %v", err)
	}
	e.must("a", func(c Ctx) (Result, error) { return Revert(c, "/x/page", 4) })

	// Move: both halves are versions; reverting the old path brings the page back there too.
	mv := e.must("a", func(c Ctx) (Result, error) { return Move(c, "/x/page", "/y/page") })
	if mv.Path != "/y/page" || mv.Version != 1 {
		t.Fatalf("move: %+v", mv)
	}
	if _, err := e.do("a", func(c Ctx) (Result, error) { _, err := Get2(c, "/x/page"); return Result{}, err }); code(err) != "no_such_page" {
		t.Fatalf("the old path after a move: %v", err)
	}
	var hist string
	e.view(chatA, func(tx store.DocsTx) error { var err error; hist, err = History(tx, "/x/page", 3, 0); return err })
	if !strings.Contains(hist, "moved to /y/page") || !strings.Contains(hist, "[deleted]") {
		t.Fatalf("history of the old path:\n%s", hist)
	}
	e.must("a", func(c Ctx) (Result, error) { return Revert(c, "/x/page", 4) })
	if _, body := e.get(chatA, "/x/page", "", 0, false); body != "v2 text" {
		t.Fatalf("the move undone: %q", body)
	}
	if _, err := e.do("a", func(c Ctx) (Result, error) { return Move(c, "/y/page", "/x/page") }); code(err) != "exists" {
		t.Fatalf("a move onto a live page: %v", err)
	}
}

// Get2 reads through the same transaction a writer would use (to prove the page is gone).
func Get2(c Ctx, path string) (string, error) {
	_, body, _, err := Get(c.Tx, path, "", 0, false)
	return body, err
}

func TestTreeListsNoBodiesFoldsDepthAndCaps(t *testing.T) {
	e := newEnv(t)
	e.must("a", put("/arch/broker", "SECRET-BODY", nil))
	e.must("a", put("/arch/db/schema", "x", nil))
	e.must("a", put("/arch/db/migrations", "x", nil))
	e.must("a", put("/notes", "x", nil))
	var all, shallow, sub string
	e.view(chatA, func(tx store.DocsTx) error {
		var err error
		if all, _, err = Tree(tx, "", 0); err != nil {
			return err
		}
		if shallow, _, err = Tree(tx, "/arch", 1); err != nil {
			return err
		}
		sub, _, err = Tree(tx, "/arch/db", 0)
		return err
	})
	if strings.Contains(all, "SECRET-BODY") || !strings.Contains(all, "/arch/broker  broker  (11 B, v1)") {
		t.Fatalf("tree:\n%s", all)
	}
	if !strings.Contains(shallow, "/arch/broker") || !strings.Contains(shallow, "/arch/db/  (2 more page(s) below)") || strings.Contains(shallow, "/arch/db/schema") {
		t.Fatalf("depth 1 under /arch:\n%s", shallow)
	}
	if strings.Contains(sub, "/notes") || strings.Contains(sub, "/arch/broker") || !strings.Contains(sub, "/arch/db/schema") {
		t.Fatalf("prefix /arch/db:\n%s", sub)
	}

	for i := 0; i < TreeLines+5; i++ {
		e.must("a", put(fmt.Sprintf("/many/p%03d", i), "x", nil))
	}
	e.view(chatA, func(tx store.DocsTx) error { var err error; all, _, err = Tree(tx, "/many", 0); return err })
	if n := strings.Count(all, "\n/many/"); n+1 > TreeLines || !strings.Contains(all, "…and 5 more") {
		t.Fatalf("the cap: %d lines\n%s", strings.Count(all, "\n"), all[len(all)-120:])
	}
}

const big = `# Guide
intro text
## Setup
setup text
### Detail
detail text
` + "```" + `
# not a heading (in a code fence)
` + "```" + `
## Usage
usage text
`

func TestLargePagesComeAsAnOutlineAndSectionsAreExact(t *testing.T) {
	e := newEnv(t)
	body := big + strings.Repeat("filler line of text\n", WholePageMax/20+50)
	e.must("a", put("/guide", body, nil))

	text, whole := e.get(chatA, "/guide", "", 0, false)
	if whole != "" || !strings.Contains(text, "too large to hand over whole") || !strings.Contains(text, "# Guide") || !strings.Contains(text, "  ## Setup") || !strings.Contains(text, "section=") {
		t.Fatalf("a large page without a section must give the outline and a hint:\n%.400s", text)
	}
	if strings.Contains(text, "not a heading") {
		t.Fatal("a line in a code fence was taken for a heading")
	}
	_, sect := e.get(chatA, "/guide", "Setup", 0, false)
	if sect != "## Setup\nsetup text\n### Detail\ndetail text\n```\n# not a heading (in a code fence)\n```\n" {
		t.Fatalf("section Setup: %q", sect)
	}
	if _, sub := e.get(chatA, "/guide", "### detail", 0, false); sub != "### Detail\ndetail text\n```\n# not a heading (in a code fence)\n```\n" {
		t.Fatalf("a subsection by a lower-case name with hashes: %q", sub)
	}
	var err error
	e.view(chatA, func(tx store.DocsTx) error { _, _, _, err = Get(tx, "/guide", "Nope", 0, false); return nil })
	if code(err) != "no_such_section" || !strings.Contains(err.Error(), "Setup | Detail | Usage") {
		t.Fatalf("missing section: %v", err)
	}
	if _, all := e.get(chatA, "/guide", "", 0, true); all != body {
		t.Fatal("full=true must return the page whole")
	}
	var out string
	e.view(chatA, func(tx store.DocsTx) error { var err error; out, err = Outline(tx, "/guide"); return err })
	if !strings.Contains(out, "## Setup  (") || !strings.Contains(out, "    ### Detail") {
		t.Fatalf("outline:\n%s", out)
	}
}

func TestSearchByPrefixIsCaseInsensitive(t *testing.T) {
	e := newEnv(t)
	e.must("a", put("/arch/broker", "# Broker\nThe BROKER keeps state in SQLite.", nil))
	e.must("a", put("/ops/runbook", "# Runbook\nrestart the broker first", nil))
	e.must("a", put("/ops/other", "# Other\nnothing here", nil))
	run := func(q, prefix string) string {
		var out string
		e.view(chatA, func(tx store.DocsTx) error { var err error; out, err = Search(tx, q, prefix); return err })
		return out
	}
	all := run("sqlite", "")
	if !strings.Contains(all, "/arch/broker  Broker: The BROKER keeps state in SQLite.") || strings.Contains(all, "/ops") {
		t.Fatalf("search:\n%s", all)
	}
	if ops := run("BROKER", "/ops"); !strings.Contains(ops, "/ops/runbook") || strings.Contains(ops, "/arch") {
		t.Fatalf("search under /ops:\n%s", ops)
	}
	if none := run("zzz", ""); !strings.Contains(none, "no matches") {
		t.Fatalf("no match: %q", none)
	}
	for i := 0; i < SearchHits+3; i++ {
		e.must("a", put(fmt.Sprintf("/lots/p%02d", i), "needle", nil))
	}
	if many := run("needle", ""); !strings.Contains(many, "…and 3 more") {
		t.Fatalf("hit cap:\n%s", many)
	}
}

func TestPathsAreChecked(t *testing.T) {
	e := newEnv(t)
	for _, bad := range []string{"", "x", "/", "/a//b", "/A", "/a/", "/a b", "/" + strings.Repeat("x", 33), "/1/2/3/4/5/6/7/8/9"} {
		if _, err := e.do("a", put(bad, "x", nil)); code(err) != "bad_path" {
			t.Fatalf("path %q: %v", bad, err)
		}
	}
	if _, err := e.do("a", put("/ok-1/seg-2", "x", nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.do("a", put("/big", strings.Repeat("x", MaxBody+1), nil)); code(err) != "too_large" {
		t.Fatalf("body size: %v", err)
	}
}

func TestChatsAreSeparateAndDataSurvivesAReopen(t *testing.T) {
	e := newEnv(t)
	e.must("a", put("/only/here", "secret", nil))
	var tree string
	e.view(chatB, func(tx store.DocsTx) error { var err error; tree, _, err = Tree(tx, "", 0); return err })
	if !strings.Contains(tree, "no pages") {
		t.Fatalf("chat B sees chat A's pages:\n%s", tree)
	}
	var err error
	e.view(chatB, func(tx store.DocsTx) error { _, _, _, err = Get(tx, "/only/here", "", 0, false); return nil })
	if code(err) != "no_such_page" {
		t.Fatalf("a page of another chat: %v", err)
	}

	e.db.Close()
	db2, derr := sqlite.Open(e.path)
	if derr != nil {
		t.Fatal(derr)
	}
	defer db2.Close()
	var body string
	if derr := db2.ViewDocs(chatA, func(tx store.DocsTx) error {
		_, b, _, gerr := Get(tx, "/only/here", "", 0, false)
		body = b
		return gerr
	}); derr != nil || body != "secret" {
		t.Fatalf("after a reopen: %q, %v", body, derr)
	}
}

func TestPagesAreACacheOfTheVersionJournal(t *testing.T) {
	e := newEnv(t)
	e.must("a", put("/a", "one", nil))
	e.must("b", put("/a", "two", v(1)))
	e.must("a", put("/b/c", "x", nil))
	e.must("a", func(c Ctx) (Result, error) { return Append(c, "/b/c", "more", nil) })
	e.must("a", func(c Ctx) (Result, error) { return Move(c, "/b/c", "/d") })
	e.must("a", func(c Ctx) (Result, error) { return Delete(c, "/a") })
	e.must("a", func(c Ctx) (Result, error) { return Revert(c, "/a", 1) })

	var versions []store.DocVersion
	live := map[string]store.Doc{}
	e.view(chatA, func(tx store.DocsTx) error {
		var err error
		if versions, err = tx.AllVersions(); err != nil {
			return err
		}
		for _, p := range []string{"/a", "/b/c", "/d"} {
			d, ok, err := tx.GetDoc(p)
			if err != nil || !ok {
				return fmt.Errorf("page %s: %v", p, err)
			}
			live[p] = d
		}
		return nil
	})
	if got := Rebuild(versions); !reflect.DeepEqual(got, live) {
		t.Fatalf("pages rebuilt from the journal differ from the table\nrebuilt: %+v\ntable:   %+v", got, live)
	}
	last := int64(0)
	for _, ver := range versions {
		if ver.Origin != origin || ver.Lamport == 0 {
			t.Fatalf("version %s v%d lacks origin or clock: %+v", ver.Path, ver.Version, ver)
		}
		if ver.Lamport <= last-1000 { // clocks grow with every write; across pages they need not be sorted
			t.Fatalf("clock %d", ver.Lamport)
		}
		last = ver.Lamport
	}
}
