package broker

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cl "github.com/PHPCraftdream/cc-quant-chat/internal/client"
	"github.com/PHPCraftdream/cc-quant-chat/internal/local"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// The client side of protected keys against a real broker: what a session does to enter a chat, and
// what it leaves on the disk of the machine.

func clientEnv(t *testing.T) (home, secrets string) {
	t.Helper()
	home, secrets = t.TempDir(), t.TempDir()
	t.Setenv("CCQC_HOME", home)
	t.Setenv("CCQC_SECRET_DIR", secrets)
	t.Setenv("CCQC_KEYSTORE", "file")
	return home, secrets
}

func (ic *invitedChat) enter(t *testing.T, c *client, nick string, in *cl.KeyJoinInput) (cl.KeyJoin, proto.Welcome, error) {
	t.Helper()
	var w proto.Welcome
	in.ChatID, in.Nick = ic.chat, nick
	kj, err := cl.JoinWithKeys(c.callE, *in, &w)
	return kj, w, err
}

func TestASessionEntersByInvitationWithAProtectedKeyAndLogsInAfterwardsAndLeavesNoPlainKey(t *testing.T) {
	clientEnv(t)
	ic := newInvitedChat(t)
	in := ic.issue(t, "bob")
	c := ic.b.connect(t)
	kj, w, err := ic.enter(t, c, "bob", &cl.KeyJoinInput{Invite: &in, SID: sidA, IDs: cl.SessionIDs{SID: sidA}})
	if err != nil || !kj.Protected || kj.Auth == nil || kj.Signer == nil || w.State.Me != "bob" {
		t.Fatalf("enroll: %v %+v", err, kj)
	}
	if !strings.Contains(kj.Note, "WARNING") { // the file store is weaker and says so
		t.Fatalf("no warning about the place of the secret: %q", kj.Note)
	}
	if refs, _ := local.NickKeyRefs(); len(refs) != 0 {
		t.Fatalf("a plain key was kept: %+v", refs)
	}
	if got, _ := local.ProtectedNicks(); len(got) != 1 || got[0] != ic.chat+"/bob" {
		t.Fatalf("protected nicks: %v", got)
	}
	// A signed write is accepted: the derived key is the nick's key.
	var sent proto.SendResult
	if err := kj.Signer.Write(c.callE, "send", "msg", &proto.SendArgs{Room: "common", Text: "hello"}, &sent); err != nil {
		t.Fatalf("a signed write: %v", err)
	}
	if code := c.call("leave", nil, nil); code != "" {
		t.Fatal(code)
	}

	// The next session: the proof of A, the same key, nothing else on the disk.
	c2 := ic.b.connect(t)
	kj2, _, err := ic.enter(t, c2, "bob", &cl.KeyJoinInput{SID: sidA, IDs: cl.SessionIDs{SID: sidA}})
	if err != nil || !kj2.Protected || !kj2.Priv.Equal(kj.Priv) {
		t.Fatalf("login: %v", err)
	}
	if refs, _ := local.NickKeyRefs(); len(refs) != 0 {
		t.Fatalf("a plain key appeared at login: %+v", refs)
	}
	c2.call("leave", nil, nil)

	// After /clear the session has a new id and says which it had; a stranger session has no chain.
	c3 := ic.b.connect(t)
	if _, _, err := ic.enter(t, c3, "bob", &cl.KeyJoinInput{SID: sidB, IDs: cl.SessionIDs{SID: sidB, PrevSID: sidA, Source: "clear"}}); err != nil {
		t.Fatalf("after /clear: %v", err)
	}
	c3.call("leave", nil, nil)
	c4 := ic.b.connect(t)
	if _, _, err := ic.enter(t, c4, "bob", &cl.KeyJoinInput{SID: sidC, IDs: cl.SessionIDs{SID: sidC}}); !proto.IsCode(err, "unlock_session") {
		t.Fatalf("another session: %v", err)
	}
}

func TestANickWithAPlainKeyMovesAtItsNextJoinAndTheOldFileAndItsCopiesAreDeadAfterwards(t *testing.T) {
	home, _ := clientEnv(t)
	ic := newInvitedChat(t)
	old := ic.plainNick(t, "bob")
	if err := local.SetNickPrivateKey(ic.chat, "bob", old); err != nil {
		t.Fatal(err)
	}
	backup, _ := os.ReadFile(filepath.Join(home, "nick-keys.json")) // a copy in some backup folder

	c := ic.b.connect(t)
	kj, _, err := ic.enter(t, c, "bob", &cl.KeyJoinInput{SID: sidA, IDs: cl.SessionIDs{SID: sidA}})
	if err != nil || !kj.Protected || !strings.Contains(kj.Note, "moved to a protected key") {
		t.Fatalf("move: %v %+v", err, kj)
	}
	if refs, _ := local.NickKeyRefs(); len(refs) != 0 {
		t.Fatalf("the old key stayed in nick-keys.json: %+v", refs)
	}
	c.call("leave", nil, nil)

	// The copy of the old file opens nothing.
	atk := ic.b.connect(t)
	if _, code := ic.join(t, atk, "bob", old, nil); code == "" {
		t.Fatal("a copy of the old nick-keys.json got in")
	}

	// A crash between the broker's move and the cleanup leaves the old key on the disk: the next login
	// succeeds by the proof of A and the leftover is removed.
	if err := os.WriteFile(filepath.Join(home, "nick-keys.json"), backup, 0o600); err != nil {
		t.Fatal(err)
	}
	c2 := ic.b.connect(t)
	if kj2, _, err := ic.enter(t, c2, "bob", &cl.KeyJoinInput{SID: sidA, IDs: cl.SessionIDs{SID: sidA}}); err != nil || !kj2.Protected {
		t.Fatalf("login with a leftover: %v", err)
	}
	if refs, _ := local.NickKeyRefs(); len(refs) != 0 {
		t.Fatalf("the leftover was not removed: %+v", refs)
	}
}

func TestALostClientSecretIsNotMadeAgainAndTheMessageSaysWhatToDo(t *testing.T) {
	_, secrets := clientEnv(t)
	ic := newInvitedChat(t)
	in := ic.issue(t, "bob")
	c := ic.b.connect(t)
	if _, _, err := ic.enter(t, c, "bob", &cl.KeyJoinInput{Invite: &in, SID: sidA}); err != nil {
		t.Fatal(err)
	}
	c.call("leave", nil, nil)
	if err := os.Remove(filepath.Join(secrets, "client-secret.key")); err != nil {
		t.Fatal(err)
	}
	_, _, err := ic.enter(t, ic.b.connect(t), "bob", &cl.KeyJoinInput{SID: sidA})
	if !errors.Is(err, local.ErrSecretLost) || !strings.Contains(err.Error(), "invitation") {
		t.Fatalf("got %v", err)
	}
	if _, serr := os.Stat(filepath.Join(secrets, "client-secret.key")); serr == nil {
		t.Fatal("a new secret was made behind the back")
	}
}

func TestAgainstABrokerThatKeepsNoProtectedKeysTheNickStaysOnItsPlainKeyAndNoSecretIsMade(t *testing.T) {
	clientEnv(t)
	b := newTestBroker(newMemStore())
	tok, id := token(t)
	k := newKey(t)
	a := b.mustJoin(t, tok, "alice", k)
	a.leaveChat()
	if err := local.SetNickPrivateKey(id, "alice", k); err != nil {
		t.Fatal(err)
	}
	c := b.connect(t)
	var w proto.Welcome
	kj, err := cl.JoinWithKeys(c.callE, cl.KeyJoinInput{ChatID: id, Token: tok, Nick: "alice"}, &w)
	if err != nil || kj.Protected || kj.Signer == nil || !strings.Contains(kj.Note, "older") {
		t.Fatalf("%v %+v", err, kj)
	}
	if !kj.Priv.Equal(ed25519.PrivateKey(k)) {
		t.Fatal("the plain key was not used")
	}
	if got, _, _ := local.ClientSecretIfAny(); got != nil {
		t.Fatal("a client secret was made for a broker that can't use it")
	}
	if refs, _ := local.NickKeyRefs(); len(refs) != 1 {
		t.Fatalf("the plain key must stay: %+v", refs)
	}
}

func TestANickOfTheTimeOfSecretsMovesThroughTheClientWithItsOldSecretAndTheSecretIsRemoved(t *testing.T) {
	clientEnv(t)
	b, db, _, _ := journalBroker(t)
	tok, id := token(t)
	legacy := b.connect(t)
	w, code := legacy.join(tok, "bob", proto.LegacyProto)
	if code != "" || w.Key == "" {
		t.Fatalf("legacy join: %q", code)
	}
	legacy.call("leave", nil, nil)
	if err := local.SetNickKey(id, "bob", w.Key); err != nil {
		t.Fatal(err)
	}
	c := b.connect(t)
	var w2 proto.Welcome
	kj, err := cl.JoinWithKeys(c.callE, cl.KeyJoinInput{ChatID: id, Token: tok, Nick: "bob"}, &w2)
	if err != nil || !kj.Protected {
		t.Fatalf("move by the old secret: %v %+v", err, kj)
	}
	if row, _ := db.ActiveNickSecret(id, "bob"); row == nil {
		t.Fatal("the broker keeps no secret for the nick")
	}
	if refs, _ := local.NickKeyRefs(); len(refs) != 0 {
		t.Fatalf("the old secret stayed: %+v", refs)
	}
	if !bytes.Equal([]byte(local.NickKey(id, "bob")), nil) {
		t.Fatal("the old secret is still readable")
	}
}
