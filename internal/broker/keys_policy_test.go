package broker

import (
	"crypto/ed25519"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// plainJoin joins with a plain key (no protection): the way in that no longer exists.
func (ic *invitedChat) plainJoin(t *testing.T, c *client, nick string, k ed25519.PrivateKey, level int) string {
	t.Helper()
	var ch proto.Challenge
	c.call("challenge", nil, &ch)
	pub := owner.Encode(k.Public().(ed25519.PublicKey))
	return c.call("join", proto.JoinArgs{Chat: ic.chat, Name: nick, Pub: pub, Proto: level,
		NickSig: owner.Encode(ed25519.Sign(k, proto.NickJoinMessage(ic.chat, nick, pub, ch.Nonce)))}, nil)
}

func TestTheOwnerRevokesAProtectedKeyAndTheNickComesBackOnlyByAnInvitation(t *testing.T) {
	ic := newInvitedChat(t)
	a := secretOf(t)
	c, _, _ := ic.enroll(t, "bob", a)

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
