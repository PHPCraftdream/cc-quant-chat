package store

import "github.com/PHPCraftdream/cc-quant-chat/internal/invite"

// ChatRef names a chat of this node.
type ChatRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Chat modes: how a chat is entered. An older chat's id is the hash of its token; a chat made by
// `ccqc chat new` has a random id and no token and is entered by invitation.
const (
	ModeToken  = "token"
	ModeInvite = "invite"
)

// Invites is the optional part of a backend that keeps invitations and the chats made for them.
type Invites interface {
	// NewChat creates a chat in invite mode (its id is random and chosen by the caller).
	NewChat(chatID, name string) error
	ChatMode(chatID string) (string, error)
	ChatName(chatID string) (string, error)
	// FindChats: the chats of this node with that name.
	FindChats(name string) ([]ChatRef, error)
	// IssueInvite keeps the record of an invitation and journals the issuing as an event of the
	// chat that carries the owner's signature of the operation.
	IssueInvite(chatID string, rec invite.Record, a AdminAttest) error
	// RevokeInvite withdraws an open invitation (false: it was not open) and journals it.
	RevokeInvite(chatID, id string, a AdminAttest) (bool, error)
	ListInvites(chatID string) ([]invite.Record, error)
}
