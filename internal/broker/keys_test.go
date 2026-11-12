package broker

import (
	"bytes"
	"crypto/ed25519"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/invite"
	"github.com/PHPCraftdream/cc-quant-chat/internal/keys"
	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// A client of protected keys as the tests play it: it holds A, the broker holds B.

func secretOf(t *testing.T) []byte {
	t.Helper()
	s, err := keys.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func b64(t *testing.T, s string) []byte {
	t.Helper()
	raw, err := owner.Decode(s)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// enroll registers a protected key for a nick by an invitation: key_prepare, the keys derived, the
// join that carries the auth key and the proof over it. It returns the connection (joined) and B.
func (ic *invitedChat) enroll(t *testing.T, nick string, a []byte) (*client, []byte, proto.Welcome) {
	t.Helper()
	in := ic.issue(t, nick)
	c := ic.b.connect(t)
	var rep proto.KeyPrepareReply
	if code := c.call("key_prepare", proto.KeyPrepareArgs{Chat: ic.chat, Nick: nick}, &rep); code != "" {
		t.Fatalf("key_prepare: %q", code)
	}
	bsec := b64(t, rep.B)
	w, code := ic.enrollJoin(t, c, nick, a, bsec, &in)
	if code != "" {
		t.Fatalf("enrolling join: %q", code)
	}
	return c, bsec, w
}

func (ic *invitedChat) enrollJoin(t *testing.T, c *client, nick string, a, bsec []byte, in *invite.Invitation) (proto.Welcome, string) {
	t.Helper()
	auth, _ := keys.AuthKey(a, ic.chat, nick)
	sign, _ := keys.SignKey(a, bsec, ic.chat, nick)
	var ch proto.Challenge
	if code := c.call("challenge", nil, &ch); code != "" {
		t.Fatalf("challenge: %q", code)
	}
	pub := owner.Encode(sign.Public().(ed25519.PublicKey))
	authPub := owner.Encode(auth.Public().(ed25519.PublicKey))
	args := proto.JoinArgs{Chat: ic.chat, Name: nick, Pub: pub, Proto: proto.KeysProto,
		NickSig: owner.Encode(ed25519.Sign(sign, proto.NickJoinMessage(ic.chat, nick, pub, ch.Nonce))),
		Invite:  &proto.JoinInvite{ID: in.ID(), Sig: owner.Encode(in.Proof([]byte(ch.Nonce), proto.InviteSubject6(ic.chat, nick, pub, authPub)))},
		Enroll:  &proto.JoinEnroll{AuthPub: authPub, SID: ic.sid}}
	var w proto.Welcome
	return w, c.call("join", args, &w)
}

// unlock proves A on the connection; it returns B and the code.
func (ic *invitedChat) unlock(t *testing.T, c *client, nick string, a []byte) ([]byte, string) {
	t.Helper()
	return ic.unlockIn(t, c, nick, a, ic.sid, "", "")
}

// unlockIn is unlock from a session: its id, and after /clear or a compaction the id it had and why.
func (ic *invitedChat) unlockIn(t *testing.T, c *client, nick string, a []byte, sid, prev, source string) ([]byte, string) {
	t.Helper()
	auth, _ := keys.AuthKey(a, ic.chat, nick)
	var ch proto.Challenge
	if code := c.call("challenge", nil, &ch); code != "" {
		t.Fatalf("challenge: %q", code)
	}
	var rep proto.KeyUnlockReply
	code := c.call("key_unlock", proto.KeyUnlockArgs{Chat: ic.chat, Nick: nick, SID: sid, PrevSID: prev, Source: source,
		AuthSig: owner.Encode(ed25519.Sign(auth, proto.KeyUnlockMessage(ic.chat, nick, ch.Nonce)))}, &rep)
	if code != "" {
		return nil, code
	}
	return b64(t, rep.B), ""
}

// joinWith joins with the key derived from A and B, at the given level.
func (ic *invitedChat) joinWith(t *testing.T, c *client, nick string, a, bsec []byte, level int) (proto.Welcome, string) {
	t.Helper()
	sign, _ := keys.SignKey(a, bsec, ic.chat, nick)
	var ch proto.Challenge
	if code := c.call("challenge", nil, &ch); code != "" {
		t.Fatalf("challenge: %q", code)
	}
	pub := owner.Encode(sign.Public().(ed25519.PublicKey))
	var w proto.Welcome
	code := c.call("join", proto.JoinArgs{Chat: ic.chat, Name: nick, Pub: pub, Proto: level,
		NickSig: owner.Encode(ed25519.Sign(sign, proto.NickJoinMessage(ic.chat, nick, pub, ch.Nonce)))}, &w)
	return w, code
}

// login is what a session does after the first time: the proof of A, then the join.
func (ic *invitedChat) login(t *testing.T, nick string, a []byte) (*client, proto.Welcome, string) {
	t.Helper()
	c := ic.b.connect(t)
	bsec, code := ic.unlock(t, c, nick, a)
	if code != "" {
		return c, proto.Welcome{}, code
	}
	w, code := ic.joinWith(t, c, nick, a, bsec, proto.KeysProto)
	return c, w, code
}

func (ic *invitedChat) leave(t *testing.T, c *client, nick string) {
	t.Helper()
	c.close()
	ic.b.waitOffline(t, ic.b.chatOf(ic.chat), nick)
}

func TestAProtectedNickJoinsOnlyAfterTheProofOfAAndNothingLessIsEnough(t *testing.T) {
	ic := newInvitedChat(t)
	a := secretOf(t)
	c, bsec, w := ic.enroll(t, "bob", a)
	if w.State.Me != "bob" {
		t.Fatalf("not joined: %+v", w.State)
	}
	// What the broker keeps is B and the public keys; the nick is in the chat under the derived key.
	row, err := ic.db.ActiveNickSecret(ic.chat, "bob")
	if err != nil || row == nil || !bytes.Equal(row.B, bsec) {
		t.Fatalf("the secret is not kept: %+v %v", row, err)
	}
	sign, _ := keys.SignKey(a, bsec, ic.chat, "bob")
	if row.SignPub != owner.Encode(sign.Public().(ed25519.PublicKey)) {
		t.Fatal("the row is for another key than the nick's")
	}
	ic.leave(t, c, "bob")

	// A and the broker's B: the way in.
	c2, w, code := ic.login(t, "bob", a)
	if code != "" || w.State.Me != "bob" {
		t.Fatalf("login: %q", code)
	}
	ic.leave(t, c2, "bob")

	// The right key without the proof of A on this connection (an attacker who has A and read B from
	// the database of the broker): not in. This is what makes the broker see every use of the key.
	c3 := ic.b.connect(t)
	if _, code := ic.joinWith(t, c3, "bob", a, bsec, proto.KeysProto); code != "key_not_unlocked" {
		t.Fatalf("without the proof: %q", code)
	}
	// A proof made on another connection is of no use on this one.
	other := ic.b.connect(t)
	if _, code := ic.unlock(t, other, "bob", a); code != "" {
		t.Fatalf("unlock: %q", code)
	}
	if _, code := ic.joinWith(t, c3, "bob", a, bsec, proto.KeysProto); code != "key_not_unlocked" {
		t.Fatalf("a proof of another connection: %q", code)
	}
	// A client of an earlier level is refused with a reason it can show.
	c4 := ic.b.connect(t)
	if _, code := ic.joinWith(t, c4, "bob", a, bsec, proto.InviteProto); code != "key_protected" {
		t.Fatalf("an earlier level: %q", code)
	}
}

func TestOnlyTheDatabaseOfTheBrokerDoesNotLetAnyoneIn(t *testing.T) {
	ic := newInvitedChat(t)
	a := secretOf(t)
	c, bsec, _ := ic.enroll(t, "bob", a)
	ic.leave(t, c, "bob")
	row, _ := ic.db.ActiveNickSecret(ic.chat, "bob")

	// Everything in the database: B, the public keys. The attacker has no A, so it can derive neither
	// the key of the nick nor the auth key; with any secret of its own the proof fails and the join
	// finds another key.
	guess := secretOf(t)
	atk := ic.b.connect(t)
	if _, code := ic.unlock(t, atk, "bob", guess); code != "unlock_denied" {
		t.Fatalf("a proof without A: %q", code)
	}
	for _, tryA := range [][]byte{guess, row.B, row.AuthPub} {
		if k, _ := keys.SignKey(tryA, row.B, ic.chat, "bob"); owner.Encode(k.Public().(ed25519.PublicKey)) == row.SignPub {
			t.Fatal("the key of the nick came out of the database alone")
		}
	}
	// A without the right B (the data of the client alone): the key does not come out either; with
	// a B of its own it joins under another key and the nick is not taken.
	c2 := ic.b.connect(t)
	if _, code := ic.unlock(t, c2, "bob", a); code != "" {
		t.Fatalf("unlock: %q", code)
	}
	if _, code := ic.joinWith(t, c2, "bob", a, secretOf(t), proto.KeysProto); code != "name_taken" {
		t.Fatalf("A with a wrong B: %q", code)
	}
	_ = bsec
}

func TestTheClientSecretAndTheKeyAreNowhereAtTheBroker(t *testing.T) {
	ic := newInvitedChat(t)
	a := secretOf(t)
	c, bsec, _ := ic.enroll(t, "bob", a)
	ic.leave(t, c, "bob")
	cc, _, code := ic.login(t, "bob", a)
	if code != "" {
		t.Fatal(code)
	}
	ic.leave(t, cc, "bob")
	sign, _ := keys.SignKey(a, bsec, ic.chat, "bob")
	auth, _ := keys.AuthKey(a, ic.chat, "bob")
	needles := [][]byte{a, sign.Seed(), auth.Seed(), []byte(owner.Encode(a)), []byte(owner.Encode(sign.Seed())), []byte(owner.Encode(auth.Seed()))}
	_ = filepath.WalkDir(filepath.Dir(ic.path), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, _ := os.ReadFile(p)
		for i, n := range needles {
			if bytes.Contains(raw, n) {
				t.Errorf("secret %d is in %s", i, p)
			}
		}
		return nil
	})
}

func TestWrongProofsAreCountedPausedAndToldToTheChat(t *testing.T) {
	ic := newInvitedChat(t)
	a := secretOf(t)
	c, _, _ := ic.enroll(t, "bob", a)
	ic.leave(t, c, "bob")
	// A watcher in the chat hears what is told.
	watchIn := ic.issue(t, "watch")
	w := ic.b.connect(t)
	if _, code := ic.join(t, w, "watch", newKey(t), &watchIn); code != "" {
		t.Fatalf("watcher: %q", code)
	}
	wrong := secretOf(t)
	for i := 1; i <= keyFailsFree; i++ {
		if _, code := ic.unlock(t, ic.b.connect(t), "bob", wrong); code != "unlock_denied" {
			t.Fatalf("proof %d: %q", i, code)
		}
	}
	// The chat was told once, at the third wrong proof; the text says nothing that can be used.
	told := 0
	deadline := time.After(2 * time.Second)
	for told == 0 {
		select {
		case ev := <-w.events:
			if ev.Kind == "notice" && strings.Contains(ev.Text, "could not prove") {
				told++
				if !strings.Contains(ev.Text, "@bob") || !strings.Contains(ev.Text, "3 wrong") {
					t.Fatalf("notice: %q", ev.Text)
				}
			}
		case <-deadline:
			t.Fatal("the chat was not told")
		}
	}
	// After keyFailsFree the right proof is paused too, whoever asks.
	if _, code := ic.unlock(t, ic.b.connect(t), "bob", a); code != "rate_limited" {
		t.Fatalf("a proof during the pause: %q", code)
	}
	// The pause ends; the right proof then works and clears the count.
	if err := ic.db.NickSecretFailed(ic.chat, "bob", keyFailsFree, time.Now().Add(-time.Second).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, code := ic.unlock(t, ic.b.connect(t), "bob", a); code != "" {
		t.Fatalf("after the pause: %q", code)
	}
	if row, _ := ic.db.ActiveNickSecret(ic.chat, "bob"); row.Fails != 0 || row.FailUntil != 0 {
		t.Fatalf("the count was not cleared: %+v", row)
	}
	n := 0
	for {
		select {
		case ev := <-w.events:
			if ev.Kind == "notice" && strings.Contains(ev.Text, "could not prove") {
				n++
			}
		case <-time.After(200 * time.Millisecond):
			if n != 0 {
				t.Fatalf("the chat was told %d more times", n)
			}
			return
		}
	}
}

func TestAnInvitationThatDoesNotWorkLeavesNoSecretAndANewInvitationReplacesTheOldProtectedKey(t *testing.T) {
	ic := newInvitedChat(t)
	a := secretOf(t)
	c, bsec, _ := ic.enroll(t, "bob", a)
	ic.leave(t, c, "bob")

	// An invitation used with a wrong proof does not touch the active secret (nor leave a pending one).
	in := ic.issue(t, "bob")
	bad := in
	bad.Seed = append([]byte(nil), in.Seed...)
	bad.Seed[0] ^= 1
	atk := ic.b.connect(t)
	var rep proto.KeyPrepareReply
	if code := atk.call("key_prepare", proto.KeyPrepareArgs{Chat: ic.chat, Nick: "bob"}, &rep); code != "" {
		t.Fatal(code)
	}
	if _, code := ic.enrollJoin(t, atk, "bob", secretOf(t), b64(t, rep.B), &bad); code != "invite_invalid" {
		t.Fatalf("a bad proof: %q", code)
	}
	if row, _ := ic.db.ActiveNickSecret(ic.chat, "bob"); row == nil || !bytes.Equal(row.B, bsec) {
		t.Fatal("the active secret was touched by a bad invitation")
	}
	if pend, _ := ic.db.PendingNickSecrets(); len(pend) != 0 {
		t.Fatalf("a pending row was left: %+v", pend)
	}

	// The real invitation replaces the key: the old A and B stop working at once.
	a2 := secretOf(t)
	rc := ic.b.connect(t)
	var rep2 proto.KeyPrepareReply
	rc.call("key_prepare", proto.KeyPrepareArgs{Chat: ic.chat, Nick: "bob"}, &rep2)
	if _, code := ic.enrollJoin(t, rc, "bob", a2, b64(t, rep2.B), &in); code != "" {
		t.Fatalf("the replacing invitation: %q", code)
	}
	ic.leave(t, rc, "bob")
	if _, _, code := ic.login(t, "bob", a); code != "unlock_denied" {
		t.Fatalf("the old A after replacement: %q", code)
	}
	if _, w, code := ic.login(t, "bob", a2); code != "" || w.State.Me != "bob" {
		t.Fatalf("the new A: %q", code)
	}
}

func TestEnrollNeedsAnInvitationAndAPreparedBAndAnInvitationThatDoesNotEnrollIsRefused(t *testing.T) {
	ic := newInvitedChat(t)
	a := secretOf(t)
	c, _, _ := ic.enroll(t, "bob", a)
	ic.leave(t, c, "bob")

	// A join that says enroll without an invitation is refused: an invitation is the only way to enroll.
	e := ic.b.connect(t)
	var rep proto.KeyPrepareReply
	e.call("key_prepare", proto.KeyPrepareArgs{Chat: ic.chat, Nick: "bob"}, &rep)
	sign, _ := keys.SignKey(a, b64(t, rep.B), ic.chat, "bob")
	auth, _ := keys.AuthKey(a, ic.chat, "bob")
	var ch proto.Challenge
	e.call("challenge", nil, &ch)
	pub := owner.Encode(sign.Public().(ed25519.PublicKey))
	if code := e.call("join", proto.JoinArgs{Chat: ic.chat, Name: "bob", Pub: pub, Proto: proto.KeysProto,
		NickSig: owner.Encode(ed25519.Sign(sign, proto.NickJoinMessage(ic.chat, "bob", pub, ch.Nonce))),
		Enroll:  &proto.JoinEnroll{AuthPub: owner.Encode(auth.Public().(ed25519.PublicKey))}}, nil); code != "bad_args" {
		t.Fatalf("enroll without an invitation: %q", code)
	}
	// Enroll without the B of key_prepare for this pair is refused before the invitation is spent.
	in := ic.issue(t, "dave")
	f := ic.b.connect(t)
	if _, code := ic.enrollJoin(t, f, "dave", a, secretOf(t), &in); code != "key_not_prepared" {
		t.Fatalf("enroll without a prepared B: %q", code)
	}
	if _, code := ic.join(t, ic.b.connect(t), "dave", newKey(t), &in); code != "" {
		t.Fatalf("the invitation was spent by a refused enroll: %q", code)
	}

	// An invitation that does not enroll a protected key (a client of the old way) is refused and not spent.
	in2 := ic.issue(t, "erin")
	var ch2 proto.Challenge
	old := ic.b.connect(t)
	old.call("challenge", nil, &ch2)
	okey := newKey(t)
	opub := owner.Encode(okey.Public().(ed25519.PublicKey))
	if code := old.call("join", proto.JoinArgs{Chat: ic.chat, Name: "erin", Pub: opub, Proto: proto.InviteProto,
		NickSig: owner.Encode(ed25519.Sign(okey, proto.NickJoinMessage(ic.chat, "erin", opub, ch2.Nonce))),
		Invite:  &proto.JoinInvite{ID: in2.ID(), Sig: owner.Encode(in2.Proof([]byte(ch2.Nonce), proto.InviteSubject(ic.chat, "erin", opub)))}}, nil); code != "key_protected" {
		t.Fatalf("an invitation without enroll: %q", code)
	}
	if _, code := ic.join(t, ic.b.connect(t), "erin", newKey(t), &in2); code != "" {
		t.Fatalf("the invitation was spent by a refused join: %q", code)
	}
}

func TestAPendingRowLeftByACrashIsSettledAtStartByWhatTheChatHolds(t *testing.T) {
	ic := newInvitedChat(t)
	a := secretOf(t)
	c, bsec, _ := ic.enroll(t, "bob", a)
	ic.leave(t, c, "bob")
	sign, _ := keys.SignKey(a, bsec, ic.chat, "bob")
	pub := owner.Encode(sign.Public().(ed25519.PublicKey))
	auth, _ := keys.AuthKey(a, ic.chat, "bob")

	// A crash after the pending row and after the key reached the chat: the row becomes the nick's.
	if _, err := ic.db.DeleteNickSecrets(ic.chat, "bob"); err != nil {
		t.Fatal(err)
	}
	row := store.NickSecret{ChatID: ic.chat, Nick: "bob", B: bsec, AuthPub: auth.Public().(ed25519.PublicKey), SignPub: pub, CreatedAt: time.Now().Unix()}
	if err := ic.db.PutPendingNickSecret(row); err != nil {
		t.Fatal(err)
	}
	ic.b.settleNickSecrets()
	if r, _ := ic.db.ActiveNickSecret(ic.chat, "bob"); r == nil || !bytes.Equal(r.B, bsec) {
		t.Fatalf("the pending row was not settled: %+v", r)
	}
	// A crash before the key reached the chat: the row is dropped and the old state stays.
	row.SignPub = "AAAA" // a key the chat never got
	if err := ic.db.PutPendingNickSecret(row); err != nil {
		t.Fatal(err)
	}
	ic.b.settleNickSecrets()
	if pend, _ := ic.db.PendingNickSecrets(); len(pend) != 0 {
		t.Fatalf("a row for a key the chat does not have stays: %+v", pend)
	}
	if r, _ := ic.db.ActiveNickSecret(ic.chat, "bob"); r == nil || r.SignPub != pub {
		t.Fatalf("the active row was lost: %+v", r)
	}
}
