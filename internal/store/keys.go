package store

// The broker's secrets for protected nick keys (docs/design-keys.md section 5). They live in the
// node's database only.

// NickSecret is the broker's half of the key of one nick in one chat.
type NickSecret struct {
	ChatID, Nick string
	State        string // "pending" or "active"
	B            []byte // the broker's secret, 32 bytes
	AuthPub      []byte // public key a client proves A with
	SignPub      string // the nick's key as registered in the chat (base64)
	SIDHash      string // SHA-256 (hex) of the session the nick is bound to; "" = not bound
	CreatedAt    int64  // unix seconds
	UnlockedAt   int64
	Fails        int   // wrong proofs in a row
	FailUntil    int64 // no proof is taken before this time
}

// NickSecrets is implemented by a backend that keeps them.
type NickSecrets interface {
	// PutPendingNickSecret writes the row of a key about to be registered (replaces a pending row of
	// the pair; an active one stays).
	PutPendingNickSecret(s NickSecret) error
	// ActivateNickSecret makes the pending row of the pair the active one, replacing the active one
	// (false: there was no pending row).
	ActivateNickSecret(chatID, nick string) (bool, error)
	// DropPendingNickSecret removes the pending row of the pair.
	DropPendingNickSecret(chatID, nick string) error
	// ActiveNickSecret is the active row of the pair (nil if none).
	ActiveNickSecret(chatID, nick string) (*NickSecret, error)
	// PendingNickSecrets lists the pending rows (for the settling at start).
	PendingNickSecrets() ([]NickSecret, error)
	// DeleteNickSecrets removes every row of the pair: its key is no longer protected, or was revoked.
	DeleteNickSecrets(chatID, nick string) (bool, error)
	// NickSecretUnlocked records a successful proof: the time, no wrong proofs in a row.
	NickSecretUnlocked(chatID, nick string, at int64) error
	// NickSecretFailed records the count of wrong proofs and the time before which none is taken.
	NickSecretFailed(chatID, nick string, fails int, until int64) error
}
