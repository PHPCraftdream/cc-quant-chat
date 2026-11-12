package broker

import (
	"crypto/ed25519"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// plainNick registers a nick with a plain key by an invitation (what every nick was before protected
// keys) and leaves the chat; it returns the private key, as nick-keys.json would hold it.
func (ic *invitedChat) plainNick(t *testing.T, nick string) ed25519.PrivateKey {
	t.Helper()
	in := ic.issue(t, nick)
	k := newKey(t)
	c := ic.b.connect(t)
	if _, code := ic.join(t, c, nick, k, &in); code != "" {
		t.Fatalf("plain nick: %q", code)
	}
	if code := c.call("leave", nil, nil); code != "" {
		t.Fatalf("leave: %q", code)
	}
	return k
}

// plainJoin joins with a plain key at a level, no invitation.
func (ic *invitedChat) plainJoin(t *testing.T, c *client, nick string, k ed25519.PrivateKey, level int) string {
	t.Helper()
	var ch proto.Challenge
	c.call("challenge", nil, &ch)
	pub := owner.Encode(k.Public().(ed25519.PublicKey))
	return c.call("join", proto.JoinArgs{Chat: ic.chat, Name: nick, Pub: pub, Proto: level,
		NickSig: owner.Encode(ed25519.Sign(k, proto.NickJoinMessage(ic.chat, nick, pub, ch.Nonce)))}, nil)
}

func TestWhenTheOwnerRequiresProtectedKeysOnlyProtectedKeysJoinAndTheWayToThemIsOpen(t *testing.T) {
	ic := newInvitedChat(t)
	plain := ic.plainNick(t, "bob")
	a := ic.session(t, "alice", sidA)

	// Off by default: a plain key joins as before, at both levels.
	for _, level := range []int{proto.InviteProto, proto.KeysProto} {
		c := ic.b.connect(t)
		if code := ic.plainJoin(t, c, "bob", plain, level); code != "" {
			t.Fatalf("off, level %d: %q", level, code)
		}
		ic.leave(t, c, "bob")
	}
	if _, code := adminCall(t, ic.adm, ic.ownk, "config_set", proto.AdminConfig{Key: "keys.require_protected", Value: "sometimes"}); code == "" {
		t.Fatal("a value that is neither on nor off was accepted")
	}
	adminSet(t, ic, "keys.require_protected", "on")

	// On: the plain key and an earlier level are refused, each with what to do about it.
	if code := ic.plainJoin(t, ic.b.connect(t), "bob", plain, proto.KeysProto); code != "key_unprotected" {
		t.Fatalf("a plain key: %q", code)
	}
	if code := ic.plainJoin(t, ic.b.connect(t), "bob", plain, proto.InviteProto); code != "key_protected" {
		t.Fatalf("an earlier level: %q", code)
	}
	in := ic.issue(t, "carol")
	if _, code := ic.join(t, ic.b.connect(t), "carol", newKey(t), &in); code != "key_protected" {
		t.Fatalf("an invitation used for a plain key: %q", code)
	}
	// An invitation that enrolls a protected key is the way in.
	if c, _, _ := ic.enroll(t, "carol", secretOf(t)); c == nil {
		t.Fatal("enroll")
	}
	// A protected nick logs in as ever.
	if _, _, code := ic.login(t, "alice", a); code != "" {
		t.Fatalf("a protected nick: %q", code)
	}
	// And the owner can turn it off again.
	adminSet(t, ic, "keys.require_protected", "off")
	other := ic.plainNick(t, "dave")
	if code := ic.plainJoin(t, ic.b.connect(t), "dave", other, proto.KeysProto); code != "" {
		t.Fatalf("off again: %q", code)
	}
}

func TestTheOwnerRevokesAProtectedKeyAndTheNickComesBackOnlyByAnInvitation(t *testing.T) {
	ic := newInvitedChat(t)
	a := secretOf(t)
	c, _, _ := ic.enroll(t, "bob", a)
	plain := ic.plainNick(t, "dave")
	_ = plain

	// Present: not now (the same rule as the reset).
	if _, code := adminCall(t, ic.adm, ic.ownk, "key_revoke", proto.AdminRole{Chat: ic.chat, Nick: "bob"}); code != "name_online" {
		t.Fatalf("an online nick: %q", code)
	}
	if code := c.call("leave", nil, nil); code != "" {
		t.Fatal(code)
	}
	if _, code := adminCall(t, ic.adm, ic.ownk, "key_revoke", proto.AdminRole{Chat: ic.chat, Nick: "dave"}); code != "not_protected" {
		t.Fatalf("a nick with a plain key: %q", code)
	}
	if _, code := adminCall(t, ic.adm, ic.ownk, "key_revoke", proto.AdminRole{Chat: ic.chat, Nick: "bob"}); code != "" {
		t.Fatalf("revoke: %q", code)
	}
	if row, _ := ic.db.ActiveNickSecret(ic.chat, "bob"); row != nil {
		t.Fatalf("the broker's half stayed: %+v", row)
	}
	// Every copy of A is dead at once: no B, and the nick is free (a new key needs an invitation).
	if _, code := ic.unlock(t, ic.b.connect(t), "bob", a); code != "unlock_unknown" {
		t.Fatalf("unlock after the revocation: %q", code)
	}
	if code := ic.plainJoin(t, ic.b.connect(t), "bob", newKey(t), proto.KeysProto); code != "invite_required" {
		t.Fatalf("a plain key after the revocation: %q", code)
	}
	if log := adminLog(t, ic); lastOf(log, "key_revoke") == "" {
		t.Fatalf("the administrator log has no key_revoke: %v", log)
	}
	// The way back.
	a2 := secretOf(t)
	c2, _, _ := ic.enroll(t, "bob", a2)
	ic.leave(t, c2, "bob")
	lc, _, code := ic.login(t, "bob", a2)
	if code != "" {
		t.Fatalf("after the new invitation: %q", code)
	}
	ic.leave(t, lc, "bob")
	if _, _, code := ic.login(t, "bob", a); code != "unlock_denied" {
		t.Fatalf("the revoked secret: %q", code)
	}
}

func TestTheStateMarksANickWhoseKeyIsNotProtectedAndOnlyThat(t *testing.T) {
	ic := newInvitedChat(t)
	ic.plainNick(t, "bob")
	c, _, _ := ic.enroll(t, "alice", secretOf(t))
	var st proto.State
	if code := c.call("state", nil, &st); code != "" {
		t.Fatalf("state: %q", code)
	}
	marks := map[string]bool{}
	for _, m := range st.Members {
		marks[m.Name] = m.KeyPlain
	}
	if !marks["bob"] || marks["alice"] || len(marks) != 2 {
		t.Fatalf("marks: %v", marks)
	}
}
