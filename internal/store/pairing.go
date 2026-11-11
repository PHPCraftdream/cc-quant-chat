package store

import (
	"encoding/json"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/invite"
)

// Inviting a node (docs/design-trust.md section 2): the part of a backend that keeps the invitations
// of nodes and the nodes that used one and wait for the owners to confirm the code.

// NodeScope is what a node invitation gives, kept in its record: the level at which this node accepts
// the invited one, and the chats offered for exchange.
type NodeScope struct {
	Trust string    `json:"trust"`
	Chats []ChatRef `json:"chats,omitempty"`
}

func (s NodeScope) String() string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

// ParseNodeScope reads the scope of a node invitation back.
func ParseNodeScope(s string) (NodeScope, error) {
	var out NodeScope
	err := json.Unmarshal([]byte(s), &out)
	return out, err
}

// PendingTTL: how long a node waits for the owners to confirm the code after the invitation was used.
const PendingTTL = 15 * time.Minute

// Pending is a node that has used an invitation (or been given one) and is not yet confirmed.
type Pending struct {
	Origin string
	Pub    []byte
	Addr   string // where to reach it, "" if only it connects
	Trust  string // what this node accepts from it
	Until  time.Time
	Code   string // the six digits the owners compare
	Chats  []ChatRef
}

// ConfirmResult is what entering a code did.
type ConfirmResult struct {
	Right  bool // the code was right: this side is confirmed
	Tries  int  // wrong codes entered so far
	Gone   bool // the third wrong code: the node was dropped
	Active bool // both sides have confirmed: the node is active now
}

// Pairing is implemented by a backend that keeps the invitations of nodes.
type Pairing interface {
	IssueNodeInvite(rec invite.Record) error
	RevokeNodeInvite(id string) (bool, error)
	ListNodeInvites() ([]invite.Record, error)
	// AcceptNodeInvite uses an invitation for the node that proved it: in one transaction it burns
	// the invitation and lists the node as pending (its chats are shared once it is active). A wrong
	// proof counts against the invitation (invite.BadProof); anything else that does not work is
	// invite.ErrInvalid. It returns the record with its scope.
	AcceptNodeInvite(id string, now time.Time, p Pending, proofOK func(invite.Record) bool) (invite.Record, error)
	// AddPendingPeer lists the node that was joined (the side that used the invitation).
	AddPendingPeer(p Pending) error
	// PendingCode: the code of a pending node.
	PendingCode(origin string) (string, bool, error)
	// PendingChats: the chats a pending node offers.
	PendingChats(origin string) ([]ChatRef, error)
	// Confirm: this node's owner enters the code of a pending node.
	Confirm(origin, code string) (ConfirmResult, error)
	// ConfirmedRemote: the node says that its owner has confirmed. It reports whether the node is
	// active now.
	ConfirmedRemote(origin string) (active bool, err error)
	// ConfirmedHere says whether this node's owner has confirmed the code of the node.
	ConfirmedHere(origin string) (bool, error)
	// DropExpired removes the pending nodes whose time is out and returns them.
	DropExpired(now time.Time) ([]Peer, error)
}
