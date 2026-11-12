package proto

import "strings"

// Protection of nick keys (docs/design-keys.md): the key of a nick is derived from a secret of the
// client (A) and a secret of the broker (B). The broker gives B out only to a client that proves it
// holds A, and a protected nick joins only on a connection that did.

// KeysProto is the level that speaks key_prepare, key_unlock and enroll.
const KeysProto = 6

// KeyPrepareArgs asks for a new B for a key about to be registered with an invitation. The broker
// keeps it in the connection until the join that registers the key, and nowhere else.
type KeyPrepareArgs struct {
	Chat string `json:"chat"` // the chat's id
	Nick string `json:"nick"`
}

// KeyPrepareReply: B, base64.
type KeyPrepareReply struct {
	B string `json:"b"`
}

// KeyUnlockArgs proves the holder of A: AuthSig is the signature, with the auth key, of
// KeyUnlockMessage over a nonce from "challenge" on this connection.
type KeyUnlockArgs struct {
	Chat    string `json:"chat"`
	Nick    string `json:"nick"`
	AuthSig string `json:"auth_sig"`
	// SID is the Claude Code session id the client runs in ("" for a terminal). A nick that was enrolled
	// by a session is bound to it. After /clear or a compaction the session has a new id: the client then
	// also says which it had (PrevSID) and why (Source: clear or compact), and the broker moves the
	// binding one step, if PrevSID is the one it has. The ids are declared by the client, not
	// attested: this guards against a copy of A in the wrong session, not against a process that
	// knows A and the id (docs/design-keys.md section 8).
	SID     string `json:"sid,omitempty"`
	PrevSID string `json:"prev_sid,omitempty"`
	Source  string `json:"source,omitempty"`
}

// KeyUnlockReply: B, base64.
type KeyUnlockReply struct {
	B string `json:"b"`
}

// JoinEnroll makes the key of a join a protected one: AuthPub is the public half of the auth key. The
// B for it is the one of key_prepare on this connection. What authorizes it is the proof next to it:
// an invitation (its subject is InviteSubject).
type JoinEnroll struct {
	AuthPub string `json:"auth_pub"`
	SID     string `json:"sid,omitempty"` // the session the nick is bound to; "" for a terminal nick
}

// KeyUnlockMessage is what a client signs with its auth key to be given B.
func KeyUnlockMessage(chatID, nick, nonce string) []byte {
	return []byte(strings.Join([]string{"ccqc/v1/key-unlock", chatID, nick, nonce}, "\n"))
}

// InviteSubject is what a use of an invitation is for: the chat, the nick, its key and its auth key.
func InviteSubject(chatID, nick, pub, authPub string) []byte {
	return []byte(strings.Join([]string{"ccqc/v1/invite-use-protected", chatID, nick, pub, authPub}, "\n"))
}
