package broker

import (
	"crypto/ed25519"
	"crypto/sha256"
	"sync"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/keys"
	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// A nick enters a chat by an invitation and after that only by its protected key: the proof of the client
// secret, then the join with the key. These helpers let a test that only has a nick key (an Ed25519 pair) do
// that through the real path: the auth key of a test nick is derived from its key, so a test that keeps the key
// alone can prove it. (The broker never checks that the key is derived from A and B: it checks the proof of A,
// the signature of the key and what it has registered.)

// testAuth is the auth key of a test nick key.
func testAuth(k ed25519.PrivateKey) ed25519.PrivateKey {
	h := sha256.Sum256(append([]byte("test-auth|"), k.Seed()...))
	return ed25519.NewKeyFromSeed(h[:])
}

// unlockAs proves the (test) auth key of k for the nick on the connection; it returns the code ("" = given B).
func unlockAs(t *testing.T, c *client, chatID, nick string, k ed25519.PrivateKey) string {
	t.Helper()
	var ch proto.Challenge
	if code := c.call("challenge", nil, &ch); code != "" {
		t.Fatalf("challenge: %q", code)
	}
	return c.call("key_unlock", proto.KeyUnlockArgs{Chat: chatID, Nick: nick,
		AuthSig: owner.Encode(ed25519.Sign(testAuth(k), proto.KeyUnlockMessage(chatID, nick, ch.Nonce)))}, nil)
}

// seedProtected registers the nick of a chat the way an enrolment leaves it: its key in the chat and the
// broker's half of it. It does nothing for a nick that is there already.
func (b *Broker) seedProtected(t *testing.T, tok, who string, k ed25519.PrivateKey) {
	t.Helper()
	name, id, err := proto.ParseToken(tok)
	if err != nil {
		t.Fatal(err)
	}
	b.seedProtectedIn(t, id, name, who, k)
}

func (b *Broker) seedProtectedIn(t *testing.T, id, chatName, who string, k ed25519.PrivateKey) {
	t.Helper()
	ss, ok := b.nickSecrets()
	if !ok {
		t.Fatal("the store keeps no secrets")
	}
	ch, err := b.useChat(id, chatName)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { b.doneChat(ch); b.unloadIfEmpty(ch) }()
	if ch.nicks[who] == nil {
		fresh := &nick{pub: pubOf(k), seen: ch.nextID}
		if e := ch.save(store.Record{T: "nick", Nick: who, Pub: fresh.pub, ID: fresh.seen}); e != nil {
			t.Fatalf("seed @%s: %v", who, e)
		}
		ch.nicks[who] = fresh
		ch.rooms[proto.RoomCommon].members[who] = true
	}
	if row, _ := ss.ActiveNickSecret(id, who); row != nil {
		return
	}
	bsec, _ := keys.NewSecret()
	row := store.NickSecret{ChatID: id, Nick: who, B: bsec, AuthPub: testAuth(k).Public().(ed25519.PublicKey), SignPub: pubOf(k)}
	if err := ss.PutPendingNickSecret(row); err != nil {
		t.Fatal(err)
	}
	if _, err := ss.ActivateNickSecret(id, who); err != nil {
		t.Fatal(err)
	}
}

// memSecrets: the broker's halves of protected keys in a memory store.
type memSecrets struct {
	mu   sync.Mutex
	rows map[string]map[string]store.NickSecret // chat/nick → state → row
}

func (m *memSecrets) key(chatID, nick string) string { return chatID + "/" + nick }

func (m *memSecrets) PutPendingNickSecret(s store.NickSecret) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rows == nil {
		m.rows = map[string]map[string]store.NickSecret{}
	}
	k := m.key(s.ChatID, s.Nick)
	if m.rows[k] == nil {
		m.rows[k] = map[string]store.NickSecret{}
	}
	s.State = "pending"
	m.rows[k]["pending"] = s
	return nil
}

func (m *memSecrets) ActivateNickSecret(chatID, nick string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(chatID, nick)
	p, ok := m.rows[k]["pending"]
	if !ok {
		return false, nil
	}
	delete(m.rows[k], "pending")
	p.State = "active"
	m.rows[k]["active"] = p
	return true, nil
}

func (m *memSecrets) DropPendingNickSecret(chatID, nick string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows[m.key(chatID, nick)], "pending")
	return nil
}

func (m *memSecrets) ActiveNickSecret(chatID, nick string) (*store.NickSecret, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.rows[m.key(chatID, nick)]["active"]; ok {
		return &r, nil
	}
	return nil, nil
}

func (m *memSecrets) PendingNickSecrets() ([]store.NickSecret, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.NickSecret
	for _, st := range m.rows {
		if p, ok := st["pending"]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func (m *memSecrets) DeleteNickSecrets(chatID, nick string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(chatID, nick)
	had := len(m.rows[k]) > 0
	delete(m.rows, k)
	return had, nil
}

func (m *memSecrets) NickSecretUnlocked(chatID, nick string, at int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(chatID, nick)
	if r, ok := m.rows[k]["active"]; ok {
		r.UnlockedAt, r.Fails, r.FailUntil = at, 0, 0
		m.rows[k]["active"] = r
	}
	return nil
}

func (m *memSecrets) NickSecretRebind(chatID, nick, sidHash string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(chatID, nick)
	r, ok := m.rows[k]["active"]
	if ok {
		r.SIDHash = sidHash
		m.rows[k]["active"] = r
	}
	return ok, nil
}

func (m *memSecrets) NickSecretFailed(chatID, nick string, fails int, until int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(chatID, nick)
	if r, ok := m.rows[k]["active"]; ok {
		r.Fails, r.FailUntil = fails, until
		m.rows[k]["active"] = r
	}
	return nil
}
