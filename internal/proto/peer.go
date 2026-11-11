package proto

// Arguments and replies of the administrator operations on the nodes this broker exchanges chats
// with (peer_add, peer_remove, peer_share, peer_unshare; peer_list is a plain request).

// PeerAdd: a node by its origin (32 hex) and where to reach it (host:port).
type PeerAdd struct {
	Origin string `json:"origin"`
	Addr   string `json:"addr"`
}

// PeerTrust: change what this node accepts from a node (read, member, trusted).
type PeerTrust struct {
	Origin string `json:"origin"`
	Level  string `json:"level"`
}

type PeerRemove struct {
	Origin string `json:"origin"`
}

// PeerShare: share a chat (its 64-hex id) with a node, or stop.
type PeerShare struct {
	Chat   string `json:"chat"`
	Origin string `json:"origin"`
}

// PeerInfo is one node in the reply to peer_list.
type PeerInfo struct {
	Origin     string   `json:"origin"`
	Addr       string   `json:"addr,omitempty"`
	Trust      string   `json:"trust"`
	State      string   `json:"state,omitempty"`    // active | pending
	Accepted   int      `json:"accepted,omitempty"` // events taken from it since the broker started
	Refused    int      `json:"refused,omitempty"`  // events not taken, by its level
	Reasons    string   `json:"reasons,omitempty"`  // why, as text
	Chats      []string `json:"chats,omitempty"`    // ids of the chats shared with it
	Connected  bool     `json:"connected"`
	Flagged    string   `json:"flagged,omitempty"`    // why it is no longer listened to
	Until      string   `json:"until,omitempty"`      // pending: when it is dropped unless confirmed (RFC 3339)
	Confirmed  bool     `json:"confirmed,omitempty"`  // pending: this node's owner has confirmed
	Sources    int      `json:"sources,omitempty"`    // sources (not neighbors here) that it has brought
	Quarantine string   `json:"quarantine,omitempty"` // in quarantine: why, since when, by whom
	Bytes      int64    `json:"bytes,omitempty"`      // taken from it today
}

type PeerList struct {
	Node        string     `json:"node"`
	Listen      string     `json:"listen,omitempty"` // where this broker accepts nodes, if it does
	Peers       []PeerInfo `json:"peers"`
	Quarantined []string   `json:"quarantined,omitempty"` // every source in quarantine, neighbors or not: origin, why, since, by
}

// Inviting a node (peer_invite, peer_invite_revoke, peer_join, peer_code, peer_confirm).

// PeerInvite: an invitation for a node. Addr is where the node is reached (default: setting
// peer.advertise); Trust what this node accepts from the one that uses it; Chats the chats (ids)
// offered for exchange once the owners have confirmed.
type PeerInvite struct {
	TTLSeconds int64    `json:"ttl,omitempty"`
	Trust      string   `json:"trust,omitempty"`
	Addr       string   `json:"addr,omitempty"`
	Chats      []string `json:"chats,omitempty"`
}

// PeerInviteID: an invitation of a node by its id (or the first characters).
type PeerInviteID struct {
	ID string `json:"id"`
}

// PeerJoin: use an invitation of another node; Trust is what this node accepts from it.
type PeerJoin struct {
	Invitation string `json:"invitation"`
	Trust      string `json:"trust,omitempty"`
}

// PeerCode: the code of a pending node (peer_code), or the code the owner entered (peer_confirm).
type PeerCode struct {
	Origin string `json:"origin"`
	Code   string `json:"code,omitempty"`
}

// PeerQuarantine: put a node (source) in quarantine with a reason, or release it.
type PeerQuarantine struct {
	Origin string `json:"origin"`
	Reason string `json:"reason,omitempty"`
}
