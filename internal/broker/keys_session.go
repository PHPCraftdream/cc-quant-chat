package broker

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// The binding of a protected nick to the Claude Code session that enrolled it (docs/design-keys.md
// section 8). The session id is declared by the client and not attested by anything, so this is a
// guard against a copy of A in the wrong session, not a border against a process that knows A and the
// id. After /clear or a compaction the session has a new id; the client then says which it had, and
// the binding moves one step along that chain.

// sessionAlertEvery: how often the chat is told of the same refused session (a change of id is told every time).
const sessionAlertEvery = time.Hour

func sidHash(sid string) string {
	if sid == "" {
		return ""
	}
	h := sha256.Sum256([]byte("ccqc/v1/sid|" + sid))
	return hex.EncodeToString(h[:])
}

// shortSID is what is shown of a session id: the first characters.
func shortSID(sid string) string {
	if len(sid) > 8 {
		return sid[:8] + "…"
	}
	if sid == "" {
		return "none"
	}
	return sid
}

// bindSession: whether another session is refused (the setting keys.bind_session; on unless the owner
// turned it off as an emergency switch).
func (b *Broker) bindSession() bool {
	v, _ := b.setting("", "keys.bind_session")
	return v != "off"
}

// nickOnline: the nick is present in its chat now (the chat is loaded and has a live connection of it).
func (b *Broker) nickOnline(chatID, nick string) bool {
	for _, ch := range b.chatList() {
		if ch.id != chatID {
			continue
		}
		ch.mu.Lock()
		m := ch.members[nick]
		on := !ch.dead && m != nil && m.conn != nil
		ch.mu.Unlock()
		return on
	}
	return false
}

// checkSession runs after the proof of A. The caller holds keyMu.
//
// A protected nick belongs to a Claude Code session. A key that is asked for with no session id, or for a nick that
// has none bound (a terminal nick, as an earlier version let one be enrolled), is refused, whatever the switch
// keys.bind_session says: that switch is about another session, not about no session. The way back is a new
// invitation, enrolled from a session.
func (b *Broker) checkSession(ss store.NickSecrets, c *conn, row *store.NickSecret, a proto.KeyUnlockArgs) *proto.Error {
	if a.SID == "" || row.SIDHash == "" {
		why := "no session id given"
		if row.SIDHash == "" {
			why = "the nick is bound to no session"
		}
		b.logSession("key.unlock", row, a, c, "refused: "+why)
		return proto.Errf("unlock_session", "@%s is a protected nick of a Claude Code session: %s; a new invitation from the owner, used in a session, enrolls it (ccqc invite %s %s)", row.Nick, why, row.ChatID[:8], row.Nick)
	}
	now := sidHash(a.SID)
	if now == row.SIDHash {
		return nil
	}
	// One step along the chain: the client says which id it had and that the session went on after /clear or
	// a compaction. Only the id the broker has counts, not "one of the earlier ones".
	if sidHash(a.PrevSID) == row.SIDHash && (a.Source == "clear" || a.Source == "compact") {
		if _, err := ss.NickSecretRebind(row.ChatID, row.Nick, now); err != nil {
			return proto.Errf("store_failed", "%v", err)
		}
		b.logSession("key.rebind", row, a, c, "ok")
		go b.announceByID(row.ChatID, "the session of @"+row.Nick+" changed its id ("+a.Source+")")
		return nil
	}
	if !b.bindSession() {
		b.logSession("key.unlock", row, a, c, "allowed: another session (binding is off)")
		b.sessionAlert(row, a)
		return nil
	}
	b.logSession("key.unlock", row, a, c, "refused: another session")
	b.sessionAlert(row, a)
	return proto.Errf("unlock_session", "@%s is bound to another session; a new invitation from the owner moves it (ccqc invite %s %s)", row.Nick, row.ChatID[:8], row.Nick)
}

// sessionAlert tells the chat that someone who holds A asked for the key from another session, at most
// once an hour per nick. The caller holds keyMu.
func (b *Broker) sessionAlert(row *store.NickSecret, a proto.KeyUnlockArgs) {
	verb := "refused"
	if !b.bindSession() {
		verb = "let in"
	}
	k := "session/" + verb + "/" + pairID(row.ChatID, row.Nick) // a change from refusing to letting in is told too
	if b.keyAlerts == nil {
		b.keyAlerts = map[string]time.Time{}
	}
	if at, seen := b.keyAlerts[k]; seen && time.Since(at) < sessionAlertEvery {
		return
	}
	b.keyAlerts[k] = time.Now()
	go b.announceByID(row.ChatID, "a login under @"+row.Nick+" from another session ("+shortSID(a.SID)+") was "+verb)
}

func (b *Broker) logSession(op string, row *store.NickSecret, a proto.KeyUnlockArgs, c *conn, result string) {
	args := map[string]string{"chat": row.ChatID[:8], "nick": row.Nick, "sid": shortSID(a.SID), "peer": c.peer.String()}
	if a.PrevSID != "" {
		args["prev"] = shortSID(a.PrevSID)
		args["source"] = a.Source
	}
	b.logAdminMap(op, args, result)
}
