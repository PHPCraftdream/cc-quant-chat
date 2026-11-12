// Package events is the unit of storage of a chat: one record per change, kept by the node that
// accepted it, chained to the node's previous record of the same chat and signed by the node.
// Everything else the broker keeps (messages, the board, documents, nicks, rooms) is a projection
// of the events and can be rebuilt from them.
//
// An event has two kinds of content. Payload is what the author sent and signed (canonical JSON of
// the request); Assigned is what the node added: ids it issued, the rows it wrote. The author's
// signature covers the payload (see proto.EventMessage); the node's covers everything.
//
// The node signs a hash in which the four contents (payload, author key, author signature,
// assigned) enter as hashes of their own. An event can therefore be passed on with those four
// replaced by their hashes (Redact): its chain and its node signature still verify, and what it
// says about itself (kind, clock, time) is checked, but the contents are not given away.
package events

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// Hidden holds the hashes (hex SHA-256) of the contents of a redacted event.
type Hidden struct {
	Payload  string `json:"payload"`
	Author   string `json:"author"`
	Sig      string `json:"sig"`
	Assigned string `json:"assigned"`
}

// Event is one record of the journal of a chat on one node.
type Event struct {
	ChatID    string  `json:"chat"`
	Origin    string  `json:"origin"`               // the node that accepted the event
	Seq       int64   `json:"seq"`                  // 1, 2, 3 ... per (chat, origin)
	Prev      string  `json:"prev"`                 // ID of the previous event of this (chat, origin); "" for the first
	Lamport   int64   `json:"lamport"`              // the chat's logical clock
	Kind      string  `json:"kind"`                 // msg, nick.*, room.*, config.set, a board op, doc.<op>, import.*
	Payload   string  `json:"payload,omitempty"`    // canonical JSON signed by the author
	AuthorPub string  `json:"author_pub,omitempty"` // base64 Ed25519 key of the author; "" for an unsigned event
	Sig       string  `json:"sig,omitempty"`        // the author's signature over proto.EventMessage; "" for an unsigned event
	Assigned  string  `json:"assigned,omitempty"`   // canonical JSON of what the node issued or wrote
	TS        string  `json:"ts"`                   // RFC 3339
	NodeSig   string  `json:"node_sig"`             // the node's signature over NodeMessage
	Hidden    *Hidden `json:"hidden,omitempty"`     // set for a redacted event: hashes of the four contents
}

func putField(buf []byte, s string) []byte {
	buf = binary.AppendUvarint(buf, uint64(len(s)))
	return append(buf, s...)
}

func hashHex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// Redacted reports whether the contents of the event are hidden.
func (e Event) Redacted() bool { return e.Hidden != nil }

// Redact returns the event with its four contents replaced by their hashes. A node signature made
// for the whole event verifies the redacted one just the same.
func (e Event) Redact() Event {
	if e.Redacted() {
		return e
	}
	r := e
	r.Hidden = &Hidden{Payload: hashHex(e.Payload), Author: hashHex(e.AuthorPub), Sig: hashHex(e.Sig), Assigned: hashHex(e.Assigned)}
	r.Payload, r.AuthorPub, r.Sig, r.Assigned = "", "", "", ""
	return r
}

// Rest is the hash of every field but origin, seq, prev and the node's signature: those four are
// signed next to it, so the signature covers all of them.
func (e Event) Rest() []byte {
	h := e.Hidden
	if h == nil {
		h = &Hidden{Payload: hashHex(e.Payload), Author: hashHex(e.AuthorPub), Sig: hashHex(e.Sig), Assigned: hashHex(e.Assigned)}
	}
	var buf []byte
	buf = putField(buf, e.ChatID)
	buf = putField(buf, fmt.Sprint(e.Lamport))
	buf = putField(buf, e.Kind)
	buf = putField(buf, e.TS)
	buf = putField(buf, h.Payload)
	buf = putField(buf, h.Author)
	buf = putField(buf, h.Sig)
	buf = putField(buf, h.Assigned)
	sum := sha256.Sum256(buf)
	return sum[:]
}

// NodeMessage is what a node signs for an event: origin, seq, prev and the hash of the rest.
func NodeMessage(origin string, seq int64, prev string, rest []byte) []byte {
	var buf []byte
	buf = putField(buf, "ccqc/v1/node-event")
	buf = putField(buf, origin)
	buf = putField(buf, fmt.Sprint(seq))
	buf = putField(buf, prev)
	return append(buf, rest...)
}

// ID names the event: the hash of what the node signed. The next event of the chain carries it
// as Prev.
func (e Event) ID() string {
	h := sha256.Sum256(NodeMessage(e.Origin, e.Seq, e.Prev, e.Rest()))
	return hex.EncodeToString(h[:])
}

// Sign sets NodeSig.
func (e *Event) Sign(priv ed25519.PrivateKey) {
	e.NodeSig = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, NodeMessage(e.Origin, e.Seq, e.Prev, e.Rest())))
}

// VerifyNode checks NodeSig against the node's public key.
func (e Event) VerifyNode(pub ed25519.PublicKey) bool {
	sig, err := base64.StdEncoding.DecodeString(e.NodeSig)
	if err != nil || len(sig) != ed25519.SignatureSize || len(pub) != ed25519.PublicKeySize {
		return false
	}
	return ed25519.Verify(pub, NodeMessage(e.Origin, e.Seq, e.Prev, e.Rest()), sig)
}

// VerifyAuthor checks the author's signature. An event without one (an event the node
// made itself, or one written before writes were signed) verifies as unsigned: ok is true, signed false; so does a redacted event,
// whose author cannot be checked.
func (e Event) VerifyAuthor() (ok, signed bool) {
	if e.Redacted() || (e.Sig == "" && e.AuthorPub == "") {
		return true, false
	}
	pub, err1 := base64.StdEncoding.DecodeString(e.AuthorPub)
	sig, err2 := base64.StdEncoding.DecodeString(e.Sig)
	if err1 != nil || err2 != nil || len(pub) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize {
		return false, true
	}
	msg := proto.EventMessage(e.ChatID, e.Origin, e.Kind, []byte(e.Payload), e.Lamport)
	if op, nonce, ok := e.adminOp(); ok { // the effect of an administrator operation: signed another way
		msg = proto.AdminMessage(op, []byte(e.Payload), nonce)
	}
	return ed25519.Verify(pub, msg, sig), true
}

// adminOp: the operation and challenge nonce the owner signed, for an event that is the effect of
// an administrator operation (the node recorded them in Assigned).
func (e Event) adminOp() (op, nonce string, ok bool) {
	var a struct {
		Admin *struct{ Op, Nonce string } `json:"admin"`
	}
	if json.Unmarshal([]byte(e.Assigned), &a) != nil || a.Admin == nil {
		return "", "", false
	}
	return a.Admin.Op, a.Admin.Nonce, true
}
