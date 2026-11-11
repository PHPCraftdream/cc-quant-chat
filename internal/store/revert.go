package store

// Reverting the contribution of a node (docs/design-trust.md section 6).

// RevertReport is what a revert did in one chat: events of the node (and of others that depended on
// them) that the projections no longer show, and how many of them were this node's own.
type RevertReport struct {
	Skipped    int
	SkippedOwn int
}

// RevertInfo is a revert recorded in the journal of a chat.
type RevertInfo struct {
	ID     string // names the revert in every chat it was made in
	Origin string
	Since  string // RFC 3339; "" for all the events of the node
	Undone bool
}

// Reverts is implemented by a backend that can build the projections of a chat without the events of a
// node. The journal keeps everything: a revert is an event, and so is undoing it.
type Reverts interface {
	// RevertNode writes the revert into the journal of the chat, with the owner's signature, and
	// builds the projections again.
	RevertNode(chatID, id, origin, since string, a AdminAttest) (RevertReport, error)
	// UndoRevert writes that the revert is withdrawn and builds the projections again.
	UndoRevert(chatID, id string, a AdminAttest) (RevertReport, error)
	// Reverts lists the reverts of a chat.
	Reverts(chatID string) ([]RevertInfo, error)
	// RevertStats: per node, how many events the reverts of the chat leave out and how many of those are
	// this node's own.
	RevertStats(chatID string) (map[string]RevertReport, error)
}
