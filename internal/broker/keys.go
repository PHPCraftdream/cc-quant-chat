package broker

import (
	"crypto/ed25519"
	"encoding/json"
	"log"
	"strconv"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/keys"
	"github.com/PHPCraftdream/cc-quant-chat/internal/owner"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// Protected nick keys (docs/design-keys.md): the broker keeps B, its half of the key of a nick, and
// gives it out only to a client that proves it holds A. A protected nick joins only on a connection
// that did (key_unlock); its signature, its events and everything downstream are as before.

const (
	keyFailsFree  = 5           // wrong proofs in a row before the pauses begin
	keyAlertAfter = 3           // wrong proofs in a row that are told to the chat
	keyAlertEvery = time.Hour   // ... at most this often per nick
	keyPauseBase  = time.Minute // the first pause; it doubles with every further wrong proof
	keyPauseMax   = time.Hour   // ... up to this
	keyPrepares   = 5           // key_prepare calls one connection may make
	keysNeedStore = "protected keys need the SQLite store (the broker runs with CCQC_STORE=memory)"
)

// prepared is the B of a key about to be registered: it lives in the connection until the join that
// registers the key, and nowhere else.
type prepared struct {
	pair string
	b    []byte
}

func pairID(chatID, nick string) string { return chatID + "/" + nick }

func (b *Broker) nickSecrets() (store.NickSecrets, bool) {
	ss, ok := b.store.(store.NickSecrets)
	return ss, ok
}

func keysUnavailable() *proto.Error { return proto.Errf("keys_unavailable", "%s", keysNeedStore) }

func validPair(chatID, nick string) *proto.Error {
	if len(chatID) != 64 || !isHex(chatID) {
		return proto.Errf("bad_args", "chat is the chat's id (64 hex characters)")
	}
	if !proto.ValidName(nick) {
		return proto.Errf("bad_name", "nick must match [a-z0-9-]{1,32}: %q", nick)
	}
	return nil
}

func isHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// keyPrepare makes a B for a key that an invitation is about to register. It is not written down: it
// waits in the connection for the join that carries the key derived from it.
func (b *Broker) keyPrepare(c *conn, a proto.KeyPrepareArgs) (any, *proto.Error) {
	if c.m != nil {
		return nil, proto.Errf("already_joined", "this connection already joined a chat")
	}
	if _, ok := b.nickSecrets(); !ok {
		return nil, keysUnavailable()
	}
	if e := validPair(a.Chat, a.Nick); e != nil {
		return nil, e
	}
	if c.prepares >= keyPrepares {
		return nil, proto.Errf("rate_limited", "too many keys prepared on this connection")
	}
	sec, err := keys.NewSecret()
	if err != nil {
		return nil, proto.Errf("internal", "%v", err)
	}
	c.prepares++
	c.prep = &prepared{pair: pairID(a.Chat, a.Nick), b: sec}
	return proto.KeyPrepareReply{B: owner.Encode(sec)}, nil
}

// keyUnlock gives B to the holder of A: it signs the challenge of this connection with the auth key.
// A wrong proof is counted; after keyFailsFree of them in a row no proof is taken for a growing time,
// and the chat is told (the proof is a signature: it can't be guessed, so this is an attacker or a
// broken client, and the owner should know).
func (b *Broker) keyUnlock(c *conn, a proto.KeyUnlockArgs) (any, *proto.Error) {
	if c.m != nil {
		return nil, proto.Errf("already_joined", "this connection already joined a chat")
	}
	ss, ok := b.nickSecrets()
	if !ok {
		return nil, keysUnavailable()
	}
	if e := validPair(a.Chat, a.Nick); e != nil {
		return nil, e
	}
	if b.nickOnline(a.Chat, a.Nick) { // B is not given for a nick that is present, nor is its binding moved
		return nil, proto.Errf("name_online", "nick @%s is already online in this chat", a.Nick)
	}
	b.keyMu.Lock()
	defer b.keyMu.Unlock()
	row, err := ss.ActiveNickSecret(a.Chat, a.Nick)
	if err != nil {
		return nil, proto.Errf("store_failed", "%v", err)
	}
	if row == nil {
		return nil, proto.Errf("unlock_unknown", "the key of @%s is not a protected one in this chat", a.Nick)
	}
	now := time.Now()
	if wait := time.Until(time.Unix(row.FailUntil, 0)); wait > 0 {
		return nil, proto.Errf("rate_limited", "too many wrong proofs for @%s; no proof is taken for %s", a.Nick, wait.Round(time.Second))
	}
	nonce, valid := c.takeNonce()
	if !valid {
		return nil, proto.Errf("bad_nick_sig", "no valid challenge on this connection (ask for a new one)")
	}
	sig := decodeSig(a.AuthSig)
	if sig == nil || !ed25519.Verify(ed25519.PublicKey(row.AuthPub), proto.KeyUnlockMessage(a.Chat, a.Nick, nonce), sig) {
		return nil, b.keyFailed(ss, c, row, now)
	}
	if e := b.checkSession(ss, c, row, a); e != nil {
		return nil, e
	}
	if err := ss.NickSecretUnlocked(a.Chat, a.Nick, now.Unix()); err != nil {
		return nil, proto.Errf("store_failed", "%v", err)
	}
	c.markUnlocked(a.Chat, a.Nick)
	b.logKey("key.unlock", a.Chat, a.Nick, c, "ok")
	return proto.KeyUnlockReply{B: owner.Encode(row.B)}, nil
}

// keyFailed counts a wrong proof; the caller holds keyMu.
func (b *Broker) keyFailed(ss store.NickSecrets, c *conn, row *store.NickSecret, now time.Time) *proto.Error {
	fails := row.Fails + 1
	var until int64
	if fails >= keyFailsFree {
		pause := keyPauseMax
		if shift := fails - keyFailsFree; shift < 7 {
			pause = min(keyPauseBase<<shift, keyPauseMax)
		}
		until = now.Add(pause).Unix()
	}
	if err := ss.NickSecretFailed(row.ChatID, row.Nick, fails, until); err != nil {
		log.Printf("broker: nick secret: %v", err)
	}
	b.logKey("key.unlock", row.ChatID, row.Nick, c, "refused: the proof does not verify")
	if fails >= keyAlertAfter {
		pair := pairID(row.ChatID, row.Nick)
		if b.keyAlerts == nil {
			b.keyAlerts = map[string]time.Time{}
		}
		if at, seen := b.keyAlerts[pair]; !seen || now.Sub(at) >= keyAlertEvery {
			b.keyAlerts[pair] = now
			go b.announceByID(row.ChatID, "someone asked for the key of @"+row.Nick+" and could not prove it ("+strconv.Itoa(fails)+" wrong proofs in a row)")
		}
	}
	return proto.Errf("unlock_denied", "the proof does not verify")
}

// logKey writes to the administrator log what happened to the key of a nick: the chat's first
// characters, the nick and who the kernel says asked (the program's name and pid), nothing secret.
func (b *Broker) logKey(op, chatID, nick string, c *conn, result string) {
	b.logAdminMap(op, map[string]string{"chat": chatID[:8], "nick": nick, "peer": c.peer.String()}, result)
}

func (b *Broker) logAdminMap(op string, m map[string]string, result string) {
	args, _ := json.Marshal(m)
	b.logAdmin(op, args, result)
}

// announceByID tells a chat, if it is loaded (nobody hears it otherwise).
func (b *Broker) announceByID(chatID, text string) {
	for _, ch := range b.chatList() {
		if ch.id != chatID {
			continue
		}
		ch.mu.Lock()
		if !ch.dead {
			b.announceChat(ch, text)
		}
		ch.mu.Unlock()
	}
}

func (c *conn) markUnlocked(chatID, nick string) {
	if c.unlocked == nil {
		c.unlocked = map[string]bool{}
	}
	c.unlocked[pairID(chatID, nick)] = true
}

// protectedJoinCheck: a nick whose key is protected joins only on a connection that proved A (and
// only with a client that knows how). An invitation is the way to replace the key, so it needs no
// proof of the old one; the owner proves itself by its own key.
func (b *Broker) protectedJoinCheck(ch *chat, c *conn, a proto.JoinArgs, nk *nick, owner bool) *proto.Error {
	if owner {
		return nil
	}
	if a.Enroll != nil && a.Invite == nil {
		return proto.Errf("bad_args", "enroll needs an invitation")
	}
	if a.Invite != nil {
		return b.refuseUnprotected(ch, a)
	}
	row, err := b.protectedRow(ch.id, a.Name)
	if err != nil {
		return proto.Errf("store_failed", "%v", err)
	}
	if row == nil {
		return b.refuseUnprotected(ch, a)
	}
	if nk == nil || nk.pub != row.SignPub { // fail closed: the protection is never dropped without a word
		b.keyMismatch(ch, c, row, nk)
		return proto.Errf("key_mismatch", "the key of @%s in this chat is not the protected one the broker holds: the owner can replace it with an invitation (ccqc invite %s %s)", a.Name, ch.name, a.Name)
	}
	if a.Proto < proto.KeysProto {
		return proto.Errf("key_protected", "the key of @%s is protected: update ccqc (a client of an earlier level can't join under it)", a.Name)
	}
	if !c.unlocked[pairID(ch.id, a.Name)] {
		return proto.Errf("key_not_unlocked", "the key of @%s is protected: prove the client secret first (key_unlock on this connection)", a.Name)
	}
	return nil
}

// protectedRow is the active secret of a nick (nil if the nick is not a protected one). Every path that
// takes the protection away (an invitation, the owner's reset) deletes the row; a row that is still
// there while the chat holds another key (or none) is a key that changed by a way that is not one of
// them, and is refused (keyMismatch).
func (b *Broker) protectedRow(chatID, nick string) (*store.NickSecret, error) {
	ss, ok := b.nickSecrets()
	if !ok {
		return nil, nil
	}
	return ss.ActiveNickSecret(chatID, nick)
}

// keyMismatch records a refused login that found the chat's key for a protected nick changed behind
// the protection's back, and tells the chat (at most once an hour per nick). The caller holds the chat's lock.
func (b *Broker) keyMismatch(ch *chat, c *conn, row *store.NickSecret, nk *nick) {
	have := "none"
	if nk != nil && nk.pub != "" {
		have = "another key"
	}
	b.logAdminMap("key.mismatch", map[string]string{"chat": ch.id[:8], "nick": row.Nick, "chat_has": have, "peer": c.peer.String()}, "refused: the key in the chat is not the protected one")
	b.keyMu.Lock()
	k := "mismatch/" + pairID(ch.id, row.Nick)
	if b.keyAlerts == nil {
		b.keyAlerts = map[string]time.Time{}
	}
	told := false
	if at, seen := b.keyAlerts[k]; !seen || time.Since(at) >= keyAlertEvery {
		b.keyAlerts[k], told = time.Now(), true
	}
	b.keyMu.Unlock()
	if told {
		b.announceChat(ch, "the key of @"+row.Nick+" in this chat is not the protected one the broker holds ("+have+"): a login was refused; the owner can replace the key with an invitation")
	}
}

// prepareEnroll checks what a join that enrolls a protected key brings and writes the pending row of
// it, before the key is registered in the chat; it consumes the B of key_prepare.
func (b *Broker) prepareEnroll(c *conn, ch *chat, a proto.JoinArgs) (store.NickSecrets, *proto.Error) {
	ss, ok := b.nickSecrets()
	if !ok {
		return nil, keysUnavailable()
	}
	if a.Proto < proto.KeysProto {
		return nil, proto.Errf("bad_args", "enroll is for clients of level %d and above", proto.KeysProto)
	}
	authPub, err := owner.Decode(a.Enroll.AuthPub)
	if err != nil || len(authPub) != ed25519.PublicKeySize {
		return nil, proto.Errf("bad_args", "enroll.auth_pub is not a base64 Ed25519 public key")
	}
	p := c.prep
	if p == nil || p.pair != pairID(ch.id, a.Name) {
		return nil, proto.Errf("key_not_prepared", "enroll needs the B of key_prepare for this chat and nick on this connection")
	}
	c.prep = nil
	row := store.NickSecret{ChatID: ch.id, Nick: a.Name, B: p.b, AuthPub: authPub, SignPub: a.Pub, SIDHash: sidHash(a.Enroll.SID), CreatedAt: time.Now().Unix()}
	if err := ss.PutPendingNickSecret(row); err != nil {
		return nil, proto.Errf("store_failed", "%v", err)
	}
	return ss, nil
}

// settleNickSecrets runs at start: a pending row is what a crash left between writing it and the key
// reaching the chat. The chat has the key: the row is the nick's; it does not: the row goes.
func (b *Broker) settleNickSecrets() {
	ss, ok := b.nickSecrets()
	is, ok2 := b.store.(store.Invites)
	if !ok || !ok2 {
		return
	}
	rows, err := ss.PendingNickSecrets()
	if err != nil {
		log.Printf("broker: nick secrets: %v", err)
		return
	}
	for _, row := range rows {
		name, err := is.ChatName(row.ChatID)
		if err != nil || name == "" {
			_ = ss.DropPendingNickSecret(row.ChatID, row.Nick)
			continue
		}
		ch, err := b.useChat(row.ChatID, name)
		if err != nil {
			log.Printf("broker: nick secrets: chat %s: %v", name, err)
			continue
		}
		if nk := ch.nicks[row.Nick]; nk != nil && nk.pub == row.SignPub {
			_, err = ss.ActivateNickSecret(row.ChatID, row.Nick)
		} else {
			err = ss.DropPendingNickSecret(row.ChatID, row.Nick)
		}
		if err != nil {
			log.Printf("broker: nick secrets: %v", err)
		}
		b.doneChat(ch)
		b.unloadIfEmpty(ch)
	}
}

// dropSecrets takes the protection away from a nick: its key is being freed (the owner's reset), so
// the row of the broker's half goes with it.
func (b *Broker) dropSecrets(chatID, nick string) {
	if ss, ok := b.nickSecrets(); ok {
		_, err := ss.DeleteNickSecrets(chatID, nick)
		logIf("remove the secret of a nick that was reset", err)
	}
}

// refuseUnprotected is the rule of keys.require_protected: once the owner turned it on, only protected
// keys join. A client that can't enroll (an earlier level), an invitation used for a plain key and a nick
// with a plain key are refused with what to do; an invitation or a move that enrolls a protected key is
// the way in.
func (b *Broker) refuseUnprotected(ch *chat, a proto.JoinArgs) *proto.Error {
	if !b.requireProtected() {
		return nil
	}
	if a.Enroll != nil {
		return nil
	}
	if a.Proto < proto.KeysProto {
		return proto.Errf("key_protected", "this broker accepts protected keys only: update ccqc (a client of an earlier level can't join)")
	}
	return proto.Errf("key_unprotected", "this broker accepts protected keys only: @%s has a plain key; `ccqc keys migrate` (or the next join with an updated ccqc) moves it, an invitation of the owner enrolls a new one", a.Name)
}

// revokeKey is the owner's operation for a protected nick whose client secret is lost or stolen: the
// broker's half of the key is deleted and the nick is freed, so every copy of A is dead at once and the
// nick comes back only by an invitation. It is `nick_reset` that insists the nick is a protected one.
func (b *Broker) revokeKey(chatID, nick string, at *store.AdminAttest) (any, *proto.Error) {
	ss, ok := b.nickSecrets()
	if !ok {
		return nil, keysUnavailable()
	}
	row, err := ss.ActiveNickSecret(chatID, nick)
	if err != nil {
		return nil, proto.Errf("store_failed", "%v", err)
	}
	if row == nil {
		return nil, proto.Errf("not_protected", "@%s has no protected key in this chat; `ccqc nick reset` frees a nick with a plain key", nick)
	}
	return b.resetNick(chatID, nick, at)
}
