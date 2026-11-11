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
