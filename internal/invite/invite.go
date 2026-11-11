// Package invite is the one-time invitation: how a person is let in (to a chat now, a node later)
// without a secret that can be used twice or kept.
//
// An invitation is an Ed25519 key for one use. Its seed (the secret) lives only in the invitation
// string; the broker keeps the public key. To use it the holder signs, with that key, a message that
// binds the use to one connection and one purpose (Proof); the signature reveals nothing and the
// broker holds nothing with which to sign. The record of an invitation says who it is for, until
// when, and counts bad attempts; using it is a single atomic step (Redeem), so of two clients with
// one invitation one gets in.
package invite

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The kinds of invitation. The string is the same for both so that the format does not change when
// nodes are invited (the address and the code come with that kind).
const (
	KindChat = "chat" // into a chat, for one nick
	KindNode = "node" // a node to another node
)

// Lifetimes: the broker checks them with its own clock.
const (
	DefaultTTL = 15 * time.Minute
	MinTTL     = time.Minute
	MaxTTL     = 7 * 24 * time.Hour
	MaxTries   = 3 // bad proofs after which an invitation is burnt
)

const (
	prefix  = "ccqc1."
	version = 1
	seedLen = ed25519.SeedSize
)

// Invitation is what the string holds.
type Invitation struct {
	V    int    `json:"v"`
	Kind string `json:"kind"`
	Seed []byte `json:"seed"` // the secret: one use
	Exp  int64  `json:"exp"`  // unix seconds; the broker's clock decides, this is for the person
	// A chat invitation: the chat (id and name) and the nick it is for.
	ChatID string `json:"chat_id,omitempty"`
	Chat   string `json:"chat,omitempty"`
	Nick   string `json:"nick,omitempty"`
	// A node invitation (later): where to reach the node, and its public key.
	Addrs []string `json:"addrs,omitempty"`
	Key   string   `json:"key,omitempty"`
}

// ErrFormat: the string is not an invitation (a typo shows here, before any network).
var ErrFormat = errors.New("this is not an invitation, or it was mistyped")

// TTL checks a requested lifetime: zero is the default.
func TTL(d time.Duration) (time.Duration, error) {
	switch {
	case d == 0:
		return DefaultTTL, nil
	case d < MinTTL || d > MaxTTL:
		return 0, fmt.Errorf("an invitation lives from %v to %v", MinTTL, MaxTTL)
	}
	return d, nil
}

// New makes an invitation of a kind with a fresh secret; fill sets the rest of it.
func New(kind string, ttl time.Duration, now time.Time, fill func(*Invitation)) (Invitation, error) {
	ttl, err := TTL(ttl)
	if err != nil {
		return Invitation{}, err
	}
	seed := make([]byte, seedLen)
	if _, err := rand.Read(seed); err != nil {
		return Invitation{}, err
	}
	in := Invitation{V: version, Kind: kind, Seed: seed, Exp: now.Add(ttl).Unix()}
	if fill != nil {
		fill(&in)
	}
	return in, in.check()
}

func (in Invitation) check() error {
	if in.V != version || len(in.Seed) != seedLen || in.Exp <= 0 {
		return ErrFormat
	}
	switch in.Kind {
	case KindChat:
		if len(in.ChatID) != 64 || strings.Trim(in.ChatID, "0123456789abcdef") != "" || in.Nick == "" {
			return ErrFormat
		}
	case KindNode:
		if in.Key == "" {
			return ErrFormat
		}
	default:
		return ErrFormat
	}
	return nil
}

func sum(payload string) string {
	h := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(h[:4])
}

// String is the invitation as it is handed over: ccqc1.<base64url of the JSON>.<checksum>.
func (in Invitation) String() string {
	raw, _ := json.Marshal(in)
	payload := base64.RawURLEncoding.EncodeToString(raw)
	return prefix + payload + "." + sum(payload)
}

// Parse reads an invitation string (spaces around it are ignored).
func Parse(s string) (Invitation, error) {
	s = strings.TrimSpace(s)
	body, ok := strings.CutPrefix(s, prefix)
	if !ok {
		return Invitation{}, ErrFormat
	}
	i := strings.LastIndexByte(body, '.')
	if i <= 0 || sum(body[:i]) != body[i+1:] {
		return Invitation{}, ErrFormat
	}
	raw, err := base64.RawURLEncoding.DecodeString(body[:i])
	if err != nil {
		return Invitation{}, ErrFormat
	}
	var in Invitation
	if err := json.Unmarshal(raw, &in); err != nil {
		return Invitation{}, ErrFormat
	}
	return in, in.check()
}

// Private is the key the invitation holds.
func (in Invitation) Private() ed25519.PrivateKey { return ed25519.NewKeyFromSeed(in.Seed) }

// Public is the key the broker keeps.
func (in Invitation) Public() ed25519.PublicKey { return in.Private().Public().(ed25519.PublicKey) }

// ID names the invitation to the broker: the hash of its public key. It is not a secret.
func (in Invitation) ID() string { return ID(in.Public()) }

// ID is the name of an invitation whose public key is pub.
func ID(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:])
}

// Short is the first characters of an id, as lists show it.
func Short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// message is what is signed: a fixed label, then the binding (what ties the proof to one connection:
// the nonce of a chat connection, the keying material of a TLS one) and the subject (what the
// invitation is used for), each with its length so that the two cannot be shifted into each other.
func message(binding, subject []byte) []byte {
	m := []byte("ccqc/invite/v1\x00")
	for _, part := range [][]byte{binding, subject} {
		m = binary.BigEndian.AppendUint32(m, uint32(len(part)))
		m = append(m, part...)
	}
	return m
}

// Proof signs a use of the invitation.
func (in Invitation) Proof(binding, subject []byte) []byte {
	return ed25519.Sign(in.Private(), message(binding, subject))
}

// Verify checks a proof against the public key of an invitation.
func Verify(pub ed25519.PublicKey, binding, subject, sig []byte) bool {
	return len(pub) == ed25519.PublicKeySize && ed25519.Verify(pub, message(binding, subject), sig)
}
