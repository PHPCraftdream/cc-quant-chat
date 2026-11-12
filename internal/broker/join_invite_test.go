package broker

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	cl "github.com/PHPCraftdream/cc-quant-chat/internal/client"
	"github.com/PHPCraftdream/cc-quant-chat/internal/invite"
	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store/sqlite"
)

// invitedChat is a broker that enters by invitation only (an owner, no leniency), with a chat made
// for invitations.
type invitedChat struct {
	b     *Broker
	db    *sqlite.DB
	path  string
	ownk  ed25519.PrivateKey
	adm   *client
	chat  string
	owner string
	sid   string // the session a protected key is enrolled from (keys_test.go)
}

func newInvitedChat(t *testing.T) *invitedChat {
	t.Helper()
	b, db, path, _ := journalBroker(t)
	b.lenientEntry = false
	k := newKey(t)
	register(t, b, k)
	adm := b.connect(t)
	if _, code := adminCall(t, adm, k, "chat_new", proto.AdminChatNew{Name: "team"}); code != "" {
		t.Fatalf("chat_new: %q", code)
	}
	var found proto.ChatFindReply
	adm.call("chat_find", proto.ChatFindArgs{Name: "team"}, &found)
	return &invitedChat{b: b, db: db, path: path, ownk: k, adm: adm, chat: found.Chats[0].ID}
}

func (ic *invitedChat) issue(t *testing.T, nick string) invite.Invitation {
	t.Helper()
	line, code := adminCall(t, ic.adm, ic.ownk, "invite_new", proto.AdminInviteNew{Chat: ic.chat, Nick: nick})
	if code != "" {
		t.Fatalf("invite_new: %q", code)
	}
	in, err := invite.Parse(line)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

// join does what a session does: with an invitation, key_prepare and the enrolment of the nick's key (its test
// auth key goes with it); without one, the proof of that auth key and the join. The nick's signature is over
// the same challenge.
func (ic *invitedChat) join(t *testing.T, c *client, nick string, key ed25519.PrivateKey, in *invite.Invitation) (proto.Welcome, string) {
	t.Helper()
	pub := owner.Encode(key.Public().(ed25519.PublicKey))
	a := proto.JoinArgs{Chat: ic.chat, Name: nick, Pub: pub, Proto: proto.KeysProto}
	if in != nil {
		if code := c.call("key_prepare", proto.KeyPrepareArgs{Chat: ic.chat, Nick: nick}, nil); code != "" {
			return proto.Welcome{}, code
		}
	} else if code := unlockAs(t, c, ic.chat, nick, key); code != "" {
		return proto.Welcome{}, code
	}
	var ch proto.Challenge
	if code := c.call("challenge", nil, &ch); code != "" {
		t.Fatalf("challenge: %q", code)
	}
	a.NickSig = owner.Encode(ed25519.Sign(key, proto.NickJoinMessage(ic.chat, nick, pub, ch.Nonce)))
	if in != nil {
		authPub := owner.Encode(testAuth(key).Public().(ed25519.PublicKey))
		a.Enroll = &proto.JoinEnroll{AuthPub: authPub, SID: ic.sid}
		a.Invite = &proto.JoinInvite{ID: in.ID(), Sig: owner.Encode(in.Proof([]byte(ch.Nonce), proto.InviteSubject6(ic.chat, nick, pub, authPub)))}
	}
	var w proto.Welcome
	code := c.call("join", a, &w)
	return w, code
}

func TestAnInvitationLetsOneNickInOnceAndTheKeyAloneLetsItInAfter(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	ic := newInvitedChat(t)
	in := ic.issue(t, "bob")
	bobKey := newKey(t)

	// Without an invitation a new nick has no way in.
	if _, code := ic.join(t, ic.b.connect(t), "bob", bobKey, nil); code != "unlock_unknown" {
		t.Fatalf("without an invitation: %q", code)
	}
	first := ic.b.connect(t)
	w, code := ic.join(t, first, "bob", bobKey, &in)
	if code != "" || w.State.Me != "bob" {
		t.Fatalf("with the invitation: %q %+v", code, w.State)
	}
	first.close()
	ic.b.waitOffline(t, ic.b.chatOf(ic.chat), "bob")
	// The same invitation again, from another connection with another key: no.
	if _, code := ic.join(t, ic.b.connect(t), "bob", newKey(t), &in); code != "invite_invalid" {
		t.Fatalf("a used invitation: %q", code)
	}
	// The key is the way in from now on: no invitation, no token.
	again := ic.b.connect(t)
	if _, code := ic.join(t, again, "bob", bobKey, nil); code != "" {
		t.Fatalf("rejoining by the key: %q", code)
	}
	if _, code := ic.join(t, ic.b.connect(t), "mallory", newKey(t), nil); code != "unlock_unknown" {
		t.Fatalf("another nick without an invitation: %q", code)
	}

	// The journal says which invitation registered the nick; the projections still rebuild.
	evs, err := ic.db.EventsAfter(ic.chat, ic.b.origin, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, e := range evs {
		kinds[e.Kind] = true
		if e.Kind == "nick.register" && !strings.Contains(e.Assigned, in.ID()) {
			t.Errorf("the registration does not name its invitation: %s", e.Assigned)
		}
	}
	if !kinds["invite.issue"] || !kinds["nick.register"] {
		t.Fatalf("the journal has %v", kinds)
	}
	consistent(t, ic.db, "an invitation used")

	// The secret is nowhere: not in a database, not in the log.
	needles := [][]byte{in.Seed, []byte(base64.RawURLEncoding.EncodeToString(in.Seed)), []byte(in.String())}
	_ = filepath.WalkDir(filepath.Dir(ic.path), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, _ := os.ReadFile(p)
		for _, n := range needles {
			if bytes.Contains(raw, n) {
				t.Errorf("the secret of the invitation is in %s", p)
			}
		}
		return nil
	})
	for _, n := range needles {
		if bytes.Contains(logged.Bytes(), n) {
			t.Error("the secret of the invitation is in the log")
		}
	}
}

func TestInvitationsThatDoNotWork(t *testing.T) {
	ic := newInvitedChat(t)
	carolIn := ic.issue(t, "carol")
	carol := ic.b.connect(t)
	if _, code := ic.join(t, carol, "carol", newKey(t), &carolIn); code != "" { // someone present, to hear
		t.Fatalf("carol: %q", code)
	}
	raw, err := sqlite.OpenRaw(chatFile(ic.path, ic.chat))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	// Another nick than the one it is for.
	in := ic.issue(t, "bob")
	if _, code := ic.join(t, ic.b.connect(t), "eve", newKey(t), &in); code != "invite_invalid" {
		t.Fatalf("an invitation for another nick: %q", code)
	}
	// An expired one.
	old := ic.issue(t, "dave")
	if _, err := raw.Exec(`UPDATE invites SET expires_at = 1 WHERE id = ?`, old.ID()); err != nil {
		t.Fatal(err)
	}
	if _, code := ic.join(t, ic.b.connect(t), "dave", newKey(t), &old); code != "invite_invalid" {
		t.Fatalf("an expired invitation: %q", code)
	}
	// A revoked one.
	gone := ic.issue(t, "erin")
	if _, code := adminCall(t, ic.adm, ic.ownk, "invite_revoke", proto.AdminInviteID{Chat: ic.chat, ID: invite.Short(gone.ID())}); code != "" {
		t.Fatalf("revoke: %q", code)
	}
	if _, code := ic.join(t, ic.b.connect(t), "erin", newKey(t), &gone); code != "invite_invalid" {
		t.Fatalf("a revoked invitation: %q", code)
	}
	// One nobody issued.
	stranger := invite.Invitation{}
	stranger, _ = invite.New(invite.KindChat, 0, time.Now(), func(i *invite.Invitation) { i.ChatID, i.Chat, i.Nick = ic.chat, "team", "frank" })
	if _, code := ic.join(t, ic.b.connect(t), "frank", newKey(t), &stranger); code != "invite_invalid" {
		t.Fatalf("an invitation of nobody: %q", code)
	}

	// Three wrong proofs burn an invitation; the fourth, right one is refused too, and the chat hears of it.
	burn := ic.issue(t, "gina")
	wrong := burn
	wrong.Seed = ic.issue(t, "gina").Seed // the id of one, the key of another
	for i := 0; i < invite.MaxTries; i++ {
		w := wrong
		in := burn
		c := ic.b.connect(t)
		var ch proto.Challenge
		c.call("key_prepare", proto.KeyPrepareArgs{Chat: ic.chat, Nick: "gina"}, nil)
		c.call("challenge", nil, &ch)
		key := newKey(t)
		pub := owner.Encode(key.Public().(ed25519.PublicKey))
		authPub := owner.Encode(testAuth(key).Public().(ed25519.PublicKey))
		_ = w
		var out proto.Welcome
		code := c.call("join", proto.JoinArgs{Chat: ic.chat, Name: "gina", Pub: pub, Proto: proto.KeysProto,
			NickSig: owner.Encode(ed25519.Sign(key, proto.NickJoinMessage(ic.chat, "gina", pub, ch.Nonce))),
			Enroll:  &proto.JoinEnroll{AuthPub: authPub},
			Invite:  &proto.JoinInvite{ID: in.ID(), Sig: owner.Encode(wrong.Proof([]byte(ch.Nonce), proto.InviteSubject6(ic.chat, "gina", pub, authPub)))}}, &out)
		if code != "invite_invalid" {
			t.Fatalf("wrong proof %d: %q", i, code)
		}
	}
	if _, code := ic.join(t, ic.b.connect(t), "gina", newKey(t), &burn); code != "invite_invalid" {
		t.Fatalf("a burnt invitation, right proof: %q", code)
	}
	heard := ""
	deadline := time.After(3 * time.Second)
	for !strings.Contains(heard, "burnt") {
		select {
		case ev := <-carol.events:
			if ev.Kind == "notice" {
				heard += ev.Text + "\n"
			}
		case <-deadline:
			t.Fatalf("the chat did not hear that an invitation was burnt: %q", heard)
		}
	}
	if !strings.Contains(heard, "@gina") || strings.Contains(heard, base64.RawURLEncoding.EncodeToString(burn.Seed)) {
		t.Fatalf("what the chat heard: %q", heard)
	}
}

func TestOfManyClientsWithOneInvitationOneGetsIn(t *testing.T) {
	ic := newInvitedChat(t)
	in := ic.issue(t, "hank")
	var wg sync.WaitGroup
	var mu sync.Mutex
	in5 := 0
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := ic.b.connect(t)
			c.lax = true
			if _, code := ic.join(t, c, "hank", newKey(t), &in); code == "" {
				mu.Lock()
				in5++
				mu.Unlock()
			} else if code != "invite_invalid" && code != "name_online" && code != "closed" && code != "timeout" {
				t.Errorf("unexpected %q", code)
			}
		}()
	}
	wg.Wait()
	if in5 != 1 {
		t.Fatalf("%d clients got in with one invitation", in5)
	}
}

func TestAnInvitationForANickThatExistsReplacesItsKeyAndTheOldKeyStopsWorking(t *testing.T) {
	ic := newInvitedChat(t)
	first := ic.issue(t, "bob")
	oldKey, newK := newKey(t), newKey(t)
	bob := ic.b.connect(t)
	if _, code := ic.join(t, bob, "bob", oldKey, &first); code != "" {
		t.Fatalf("bob: %q", code)
	}
	watcher := ic.issue(t, "ivy")
	ivy := ic.b.connect(t)
	if _, code := ic.join(t, ivy, "ivy", newKey(t), &watcher); code != "" {
		t.Fatalf("ivy: %q", code)
	}
	bob.close()
	ic.b.waitOffline(t, ic.b.chatOf(ic.chat), "bob")

	again := ic.issue(t, "bob")
	replaced := ic.b.connect(t)
	if _, code := ic.join(t, replaced, "bob", newK, &again); code != "" {
		t.Fatalf("the key replaced: %q", code)
	}
	replaced.close()
	ic.b.waitOffline(t, ic.b.chatOf(ic.chat), "bob")
	if _, code := ic.join(t, ic.b.connect(t), "bob", oldKey, nil); code != "unlock_denied" {
		t.Fatalf("the old key after the replacement: %q", code)
	}
	if _, code := ic.join(t, ic.b.connect(t), "bob", newK, nil); code != "" {
		t.Fatalf("the new key: %q", code)
	}
	heard := ""
	deadline := time.After(3 * time.Second)
	for !strings.Contains(heard, "replaced") {
		select {
		case ev := <-ivy.events:
			if ev.Kind == "notice" {
				heard += ev.Text + "\n"
			}
		case <-deadline:
			t.Fatalf("the chat did not hear of the new key: %q", heard)
		}
	}
	consistent(t, ic.db, "a key replaced")
}

func TestTooManyInvitationsOnOneConnectionAreRefusedWhatEverTheirIds(t *testing.T) {
	ic := newInvitedChat(t)
	c := ic.b.connect(t)
	nobody, _ := invite.New(invite.KindChat, 0, time.Now(), func(i *invite.Invitation) { i.ChatID, i.Chat, i.Nick = ic.chat, "team", "zed" })
	var last string
	for i := 0; i < inviteTriesPerMinute+1; i++ {
		_, last = ic.join(t, c, "zed", newKey(t), &nobody)
		if i < inviteTriesPerMinute && last != "invite_invalid" {
			t.Fatalf("try %d: %q", i, last)
		}
	}
	if last != "rate_limited" {
		t.Fatalf("one try too many: %q", last)
	}
}

// The client's own code (what a session runs) makes the proof the broker wants.
func TestTheProofOfTheClientCodeIsTheOneTheBrokerChecks(t *testing.T) {
	t.Setenv("CCQC_HOME", t.TempDir())
	t.Setenv("CCQC_SECRET_DIR", t.TempDir())
	t.Setenv("CCQC_KEYSTORE", "file")
	ic := newInvitedChat(t)
	in := ic.issue(t, "kim")
	enroll := func(c *client, nick string, in invite.Invitation, w *proto.Welcome) error {
		_, _, err := cl.JoinEnrolled(c.callE, cl.Enroll{ChatID: ic.chat, Nick: nick, A: func() ([]byte, error) { return secretOf(t), nil }, Invite: &in}, w)
		return err
	}
	c := ic.b.connect(t)
	var w proto.Welcome
	if err := enroll(c, "kim", in, &w); err != nil || w.State.Me != "kim" {
		t.Fatalf("%v %+v", err, w.State)
	}
	c.close()
	ic.b.waitOffline(t, ic.b.chatOf(ic.chat), "kim")
	var w2 proto.Welcome
	if err := enroll(ic.b.connect(t), "kim", in, &w2); codeOf(err) != "invite_invalid" {
		t.Fatalf("the same invitation again: %v", err)
	}
	wrongNick := ic.issue(t, "lee")
	if err := enroll(ic.b.connect(t), "kim", wrongNick, &w2); codeOf(err) != "invite_invalid" {
		t.Fatalf("an invitation for another nick: %v", err)
	}
}
