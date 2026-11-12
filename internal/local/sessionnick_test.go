package local

import (
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

func entryOf(t *testing.T, folder, sid string) Entry {
	t.Helper()
	es, err := SessionChats(folder, sid)
	if err != nil || len(es) != 1 {
		t.Fatalf("%s: %v %v", sid, es, err)
	}
	return es[0]
}

func TestTheIdTheBrokerHasSurvivesTheMovesOfASessionAndIsClearedOnceTheBrokerMovedWithIt(t *testing.T) {
	t.Setenv("CCQC_HOME", t.TempDir())
	folder := t.TempDir()
	chat := "c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1c1"
	if err := RememberChat(folder, "s1", Entry{ChatID: chat, Chat: "team", Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	if err := SetBinding(folder, "s1", "team"); err != nil {
		t.Fatal(err)
	}
	if e := entryOf(t, folder, "s1"); !e.Protected || e.Bound != "" {
		t.Fatalf("%+v", e)
	}
	// /clear: the broker has not heard of it (the nick is online), the entry remembers what it has.
	if err := MoveSession(folder, "s1", "s2", "clear"); err != nil {
		t.Fatal(err)
	}
	if e := entryOf(t, folder, "s2"); e.Bound != "s1" || e.BoundVia != "clear" {
		t.Fatalf("after /clear: %+v", e)
	}
	// A second move before the broker was told: still the id the broker has, the latest reason.
	if err := MoveSession(folder, "s2", "s3", "compact"); err != nil {
		t.Fatal(err)
	}
	if e := entryOf(t, folder, "s3"); e.Bound != "s1" || e.BoundVia != "compact" {
		t.Fatalf("after a second move: %+v", e)
	}
	// The broker moved its binding (a login): nothing is left to tell.
	if err := SetBinding(folder, "s3", "team"); err != nil {
		t.Fatal(err)
	}
	if e := entryOf(t, folder, "s3"); e.Bound != "" || e.BoundVia != "" || !e.Protected {
		t.Fatalf("after the login: %+v", e)
	}
	// A chat that is not protected carries no chain.
	if err := RememberChat(folder, "s3", Entry{ChatID: chat[:63] + "d", Chat: "other", Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	if err := MoveSession(folder, "s3", "s4", "clear"); err != nil {
		t.Fatal(err)
	}
	es, _ := SessionChats(folder, "s4")
	for _, e := range es {
		if e.Chat == "other" && (e.Bound != "" || e.BoundVia != "") {
			t.Fatalf("a plain chat got a chain: %+v", e)
		}
	}
}

func TestTheSessionThatHoldsANickIsFoundAndBoundAndAProtectedNickIsRecordedAndItsPlainKeyDropped(t *testing.T) {
	t.Setenv("CCQC_HOME", t.TempDir())
	chat := "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"
	if err := RememberChat(t.TempDir(), "old", Entry{ChatID: chat, Chat: "team", Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	newer := t.TempDir()
	if err := RememberChat(newer, "new", Entry{ChatID: chat, Chat: "team", Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	if err := update(newer, func(f *folderFile) { f.Sessions["new"].Used = "2099-01-01T00:00:00Z" }); err != nil {
		t.Fatal(err)
	}
	if got := NickSession(chat, "bob"); got != "new" {
		t.Fatalf("the most recent session: %q", got)
	}
	if got := NickSession(chat, "nobody"); got != "" {
		t.Fatalf("a nick no session has: %q", got)
	}
	if err := BindNick(chat, "bob"); err != nil {
		t.Fatal(err)
	}
	if e := entryOf(t, newer, "new"); !e.Protected {
		t.Fatalf("not bound: %+v", e)
	}

	// nick-keys.json: a plain key and an old secret; protection removes both and records the nick.
	if err := SetNickKey(chat, "bob", "old-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := NickPrivateKey(chat, "bob"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := PlainNickKey(chat, "bob"); !ok {
		t.Fatal("no plain key")
	}
	if err := MarkProtected(chat, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := DropNickKeys(chat, "bob"); err != nil {
		t.Fatal(err)
	}
	if refs, _ := NickKeyRefs(); len(refs) != 0 {
		t.Fatalf("left: %+v", refs)
	}
	if got, _ := ProtectedNicks(); len(got) != 1 || got[0] != chat+"/bob" {
		t.Fatalf("protected: %v", got)
	}
	if _, ok, _ := PlainNickKey(chat, "bob"); ok {
		t.Fatal("the plain key is still there")
	}
}

func TestTheTokenOfAChatOfTokensIsFoundByTheChatsId(t *testing.T) {
	t.Setenv("CCQC_HOME", t.TempDir())
	tok, err := proto.NewToken("team")
	if err != nil {
		t.Fatal(err)
	}
	_, id, _ := proto.ParseToken(tok)
	if err := RememberChat(t.TempDir(), "s", Entry{Token: tok, Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	if got, ok := TokenOfChat(id); !ok || got != tok {
		t.Fatalf("%q %v", got, ok)
	}
	if got := NickSession(id, "bob"); got != "s" {
		t.Fatalf("a token entry is the chat's too: %q", got)
	}
	if _, ok := TokenOfChat("0000"); ok {
		t.Fatal("a token for an id that is not its")
	}
}
