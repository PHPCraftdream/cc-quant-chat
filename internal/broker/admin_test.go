package broker

import (
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// adminReq signs an administrator operation as `ccqc` does and sends it on c.
func adminReq(t *testing.T, c *client, k ed25519.PrivateKey, op string, args any) string {
	t.Helper()
	var ch proto.Challenge
	if code := c.call("challenge", nil, &ch); code != "" {
		t.Fatalf("challenge: %q", code)
	}
	raw, _ := json.Marshal(args)
	canon, _ := proto.CanonicalJSON(raw)
	sig := owner.Encode(ed25519.Sign(k, proto.AdminMessage(op, canon, ch.Nonce)))
	return c.call("admin", proto.AdminArgs{Op: op, Args: raw, Sig: sig}, nil)
}

func results(be *memStore) []string {
	be.mu.Lock()
	defer be.mu.Unlock()
	var out []string
	for _, e := range be.admin {
		out = append(out, e.Op+": "+e.Result)
	}
	return out
}

func TestAdministratorOperationsNeedTheOwnersSignature(t *testing.T) {
	be := newMemStore()
	b := newTestBroker(be)
	tok, id := token(t)
	a := b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	args := proto.AdminRole{Chat: id, Nick: "alice", Role: "lead"}
	raw, _ := json.Marshal(args)

	// No owner yet: unavailable, and the refusal says what to do.
	if code := adminReq(t, a, newKey(t), "role_give", args); code != "no_owner" {
		t.Fatalf("without an owner: %q", code)
	}
	k := newKey(t)
	register(t, b, k)

	c := b.connect(t)
	if code := c.call("admin", proto.AdminArgs{Op: "role_give", Args: raw}, nil); code != "admin_only" {
		t.Fatalf("no signature: %q", code)
	}
	if code := adminReq(t, c, newKey(t), "role_give", args); code != "admin_only" {
		t.Fatalf("another key: %q", code)
	}
	// A valid signature for other arguments (the arguments were altered after signing).
	var ch proto.Challenge
	c.call("challenge", nil, &ch)
	good, _ := json.Marshal(proto.AdminRole{Chat: id, Nick: "alice", Role: "intern"})
	canon, _ := proto.CanonicalJSON(good)
	sig := owner.Encode(ed25519.Sign(k, proto.AdminMessage("role_give", canon, ch.Nonce)))
	if code := c.call("admin", proto.AdminArgs{Op: "role_give", Args: raw, Sig: sig}, nil); code != "admin_only" {
		t.Fatalf("altered arguments: %q", code)
	}
	// A signature for another operation.
	c.call("challenge", nil, &ch)
	canon, _ = proto.CanonicalJSON(raw)
	sig = owner.Encode(ed25519.Sign(k, proto.AdminMessage("role_take", canon, ch.Nonce)))
	if code := c.call("admin", proto.AdminArgs{Op: "role_give", Args: raw, Sig: sig}, nil); code != "admin_only" {
		t.Fatalf("another operation: %q", code)
	}
	// Replay: a challenge serves one attempt.
	c.call("challenge", nil, &ch)
	sig = owner.Encode(ed25519.Sign(k, proto.AdminMessage("role_give", canon, ch.Nonce)))
	if code := c.call("admin", proto.AdminArgs{Op: "role_give", Args: raw, Sig: sig}, nil); code != "" {
		t.Fatalf("the genuine request: %q", code)
	}
	if code := c.call("admin", proto.AdminArgs{Op: "role_take", Args: raw, Sig: sig}, nil); code != "admin_only" {
		t.Fatalf("replayed nonce: %q", code)
	}
	// The refusal speaks to an agent.
	_, perr := func() (string, string) {
		var r proto.BoardReply
		code := c.call("admin", proto.AdminArgs{Op: "role_give", Args: raw}, &r)
		return r.Text, code
	}()
	if perr != "admin_only" {
		t.Fatalf("code %q", perr)
	}

	got := results(be)
	want := []string{"role_give: refused: no owner", "role_give: refused: no valid signature", "role_give: refused: the signature does not verify",
		"role_give: refused: the signature does not verify", "role_give: refused: the signature does not verify", "role_give: ok",
		"role_take: refused: no valid challenge", "role_give: refused: no valid signature"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("admin_log:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestAdminRefusalIsAddressedToTheAgent(t *testing.T) {
	b := newTestBroker(newMemStore())
	tok, _ := token(t)
	a := b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	register(t, b, newKey(t))
	f := b.connect(t)
	if code := f.call("admin", proto.AdminArgs{Op: "config_set", Args: json.RawMessage(`{"key":"broker.idle","value":"1s"}`)}, nil); code != "admin_only" {
		t.Fatalf("code %q", code)
	}
	if !strings.Contains(adminRefusal, "do not look for a way around") || !strings.Contains(adminRefusal, "ask the user") {
		t.Fatal("the refusal must tell an agent not to look for a way around and to ask the user")
	}
}

func TestPinnedRoleCantBeChangedReleasedOrTaken(t *testing.T) {
	be := newMemStore()
	b := newTestBroker(be)
	tok, id := token(t)
	k := newKey(t)
	register(t, b, k)
	boss, other, adm := b.connect(t), b.connect(t), b.connect(t)
	boss.join(tok, "boss", proto.ProtoVersion)
	other.join(tok, "other", proto.ProtoVersion)
	boss.msgs()
	other.msgs()

	if code := adminReq(t, adm, k, "role_give", proto.AdminRole{Chat: id, Nick: "boss", Role: "lead"}); code != "" {
		t.Fatalf("give: %q", code)
	}
	st := memberInfo(t, other, "boss")
	if st.Role != "lead" || !st.RolePinned {
		t.Fatalf("state of the pinned member: %+v", st)
	}
	if n := noticeEvents(other); len(n) != 1 || !n[0].Wake || !strings.Contains(n[0].Text, "pinned the role lead to @boss") {
		t.Fatalf("the pinning is announced to the chat, loudly: %+v", n)
	}
	// Its messages carry the starred role, and nobody can write the star themselves.
	boss.send(proto.RoomCommon, "", "I decide")
	var h proto.HistoryResult
	other.call("history", proto.HistoryArgs{Room: proto.RoomCommon, Limit: 1}, &h)
	if len(h.Messages) != 1 || h.Messages[0].FromRole != "lead*" {
		t.Fatalf("history: %+v", h.Messages)
	}
	if code := other.call("role_claim", proto.RoleArgs{Role: "lead*"}, nil); code != "bad_name" {
		t.Fatalf("claiming a starred role: %q", code)
	}

	// The holder can neither change nor release it; others can't take the name.
	if code := boss.call("role_claim", proto.RoleArgs{Role: "intern"}, nil); code != "role_pinned" {
		t.Fatalf("changing a pinned role: %q", code)
	}
	if code := boss.call("role_release", nil, nil); code != "role_pinned" {
		t.Fatalf("releasing a pinned role: %q", code)
	}
	if code := other.call("role_claim", proto.RoleArgs{Role: "lead"}, nil); code != "role_pinned" {
		t.Fatalf("taking a pinned name: %q", code)
	}
	if code := other.call("role_claim", proto.RoleArgs{Role: "dev"}, nil); code != "" {
		t.Fatalf("an ordinary role still works: %q", code)
	}

	// It survives a broker restart and shows while the member is away.
	b2 := newTestBroker(be)
	if err := b2.loadOwner(); err != nil {
		t.Fatal(err)
	}
	o2 := b2.connect(t)
	o2.key = other.key
	o2.join(tok, "other", proto.ProtoVersion)
	if m := memberInfo(t, o2, "boss"); m.Role != "lead" || !m.RolePinned || m.Online {
		t.Fatalf("after a restart, an absent pinned member: %+v", m)
	}
	boss2 := b2.connect(t)
	boss2.key = boss.key
	if _, code := boss2.join(tok, "boss", proto.ProtoVersion); code != "" {
		t.Fatalf("rejoin: %q", code)
	}
	if m := memberInfo(t, o2, "boss"); m.Role != "lead" || !m.RolePinned || !m.Online {
		t.Fatalf("the pinned role comes back with the member: %+v", m)
	}

	// Taking it back frees the name and the member.
	adm2 := b2.connect(t)
	if code := adminReq(t, adm2, k, "role_take", proto.AdminRole{Chat: id, Nick: "boss"}); code != "" {
		t.Fatalf("take: %q", code)
	}
	if m := memberInfo(t, o2, "boss"); m.Role != "" || m.RolePinned {
		t.Fatalf("after taking the role: %+v", m)
	}
	if code := o2.call("role_claim", proto.RoleArgs{Role: "lead"}, nil); code != "" {
		t.Fatalf("the name is free again: %q", code)
	}
}

func TestConfigSetAppliesWithoutARestart(t *testing.T) {
	be := newMemStore()
	b := newTestBroker(be)
	tok, id := token(t)
	k := newKey(t)
	register(t, b, k)
	a, adm := b.connect(t), b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)

	if code := adminReq(t, adm, k, "config_set", proto.AdminConfig{Key: "limits.messages_per_minute", Value: "2"}); code != "" {
		t.Fatalf("set: %q", code)
	}
	for i := 0; i < 2; i++ {
		if _, code := a.send(proto.RoomCommon, "", "hi"); code != "" {
			t.Fatalf("message %d: %q", i, code)
		}
	}
	if _, code := a.send(proto.RoomCommon, "", "one too many"); code != "rate_limited" {
		t.Fatalf("the new limit must apply at once: %q", code)
	}
	if code := adminReq(t, adm, k, "config_set", proto.AdminConfig{Key: "broker.idle", Value: "banana"}); code != "bad_args" {
		t.Fatalf("a bad duration: %q", code)
	}
	if code := adminReq(t, adm, k, "config_set", proto.AdminConfig{Key: "nope", Value: "1"}); code != "bad_args" {
		t.Fatalf("unknown key: %q", code)
	}
	if code := adminReq(t, adm, k, "config_set", proto.AdminConfig{Key: "board.manager_role", Value: "lead"}); code != "bad_args" {
		t.Fatalf("a chat setting without a chat: %q", code)
	}
	if code := adminReq(t, adm, k, "config_set", proto.AdminConfig{Chat: id, Key: "board.manager_role", Value: "lead"}); code != "" {
		t.Fatalf("a chat setting: %q", code)
	}
	if code := adminReq(t, adm, k, "config_set", proto.AdminConfig{Key: "broker.idle", Value: "5m"}); code != "" {
		t.Fatalf("idle: %q", code)
	}
	b.mu.Lock()
	idle, role := b.idleFor(), b.managerRole(id)
	b.mu.Unlock()
	if idle.String() != "5m0s" || role != "lead" {
		t.Fatalf("effective values: idle %v, manager role %q", idle, role)
	}

	var list proto.ConfigList
	a.call("config_list", proto.AdminConfig{Chat: id}, &list)
	seen := map[string]proto.ConfigItem{}
	for _, it := range list.Items {
		seen[it.Key] = it
	}
	if seen["limits.messages_per_minute"].Value != "2" || seen["limits.messages_per_minute"].Source != "setting" ||
		seen["limits.messages_per_minute"].Default != "30" || seen["board.manager_role"].Value != "lead" || seen["broker.grace"].Source == "setting" {
		t.Fatalf("config list: %+v", seen)
	}

	// Stored: a new broker on the same store sees the settings.
	b2 := newTestBroker(be)
	if err := b2.loadSettings(); err != nil {
		t.Fatal(err)
	}
	if b2.msgLimit() != 2 || b2.managerRole(id) != "lead" {
		t.Fatalf("after a restart: limit %d, manager role %q", b2.msgLimit(), b2.managerRole(id))
	}
}

func TestNickResetBecomesAnAdministratorOperationOnceThereIsAnOwner(t *testing.T) {
	be := newMemStore()
	b := newTestBroker(be)
	tok, id := token(t)
	bob := b.connect(t)
	bob.join(tok, "bob", proto.ProtoVersion)
	bob.close()
	b.waitOffline(t, b.chatOf(id), "bob")
	b.inChat(id, func(ch *chat) { delete(ch.members, "bob") })

	token := func() string {
		c := b.connect(t)
		return c.call("nick_reset", proto.NickResetArgs{Token: tok, Name: "bob"}, nil)
	}
	k := newKey(t)
	register(t, b, k)
	if code := token(); code != "admin_only" {
		t.Fatalf("a token holder resetting a nick while an owner exists: %q", code)
	}
	adm := b.connect(t)
	if code := adminReq(t, adm, k, "nick_reset", proto.AdminRole{Chat: id, Nick: "bob"}); code != "" {
		t.Fatalf("the owner's reset: %q", code)
	}
	fresh := b.connect(t)
	if w, code := fresh.join(tok, "bob", proto.ProtoVersion); code != "" || w.Key == "" {
		t.Fatalf("join after the reset: %q %q", code, w.Key)
	}
	got := results(be)
	if len(got) < 2 || got[0] != "nick_reset: refused: needs the owner's signature" || got[len(got)-1] != "nick_reset: ok" {
		t.Fatalf("admin_log: %v", got)
	}
}
