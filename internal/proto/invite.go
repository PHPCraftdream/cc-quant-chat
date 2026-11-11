package proto

// Administrator operations on chats and invitations (docs/design-invites.md); chat_find is not one.

// AdminChatNew: `ccqc chat new <name>`.
type AdminChatNew struct {
	Name string `json:"name"`
}

// AdminInviteNew: `ccqc invite <chat> <nick>`; the chat is its 64-hex id.
type AdminInviteNew struct {
	Chat       string `json:"chat"`
	Nick       string `json:"nick"`
	TTLSeconds int64  `json:"ttl_seconds,omitempty"` // 0: the default
}

// AdminInviteID: revoke or list; ID is the first 8 or more characters of an invitation id ("" for a list).
type AdminInviteID struct {
	Chat string `json:"chat"`
	ID   string `json:"id,omitempty"`
}

// ChatFindArgs asks which chats of this node have a name.
type ChatFindArgs struct {
	Name string `json:"name"`
}

// ChatRef names a chat.
type ChatRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ChatFindReply: the chats with that name, in the order they were made; none or several is for the
// caller to say, nothing is chosen silently.
type ChatFindReply struct {
	Chats []ChatRef `json:"chats"`
}

// JoinInvite: ID names the invitation (the hash of its public key); Sig is the signature, with the
// key the invitation holds, of InviteSubject bound to the challenge of the connection.
type JoinInvite struct {
	ID  string `json:"id"`
	Sig string `json:"sig"`
}

// SigningProto is the level from which clients sign their events with a nick key (proto 4); a
// client of this level or above is refused an unsigned write. InviteProto is the level that joins a
// chat by its id and, for a new nick, by an invitation (docs/design-invites.md).
const (
	SigningProto = 4
	InviteProto  = 5
)
