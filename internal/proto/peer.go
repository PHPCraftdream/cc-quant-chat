package proto

// Arguments and replies of the administrator operations on the nodes this broker exchanges chats
// with (peer_add, peer_remove, peer_share, peer_unshare; peer_list is a plain request).

// PeerAdd: a node by its origin (32 hex) and where to reach it (host:port).
type PeerAdd struct {
	Origin string `json:"origin"`
	Addr   string `json:"addr"`
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
	Origin    string   `json:"origin"`
	Addr      string   `json:"addr,omitempty"`
	Trust     string   `json:"trust"`
	Chats     []string `json:"chats,omitempty"` // ids of the chats shared with it
	Connected bool     `json:"connected"`
	Flagged   string   `json:"flagged,omitempty"` // why it is no longer listened to
}

type PeerList struct {
	Node   string     `json:"node"`
	Listen string     `json:"listen,omitempty"` // where this broker accepts nodes, if it does
	Peers  []PeerInfo `json:"peers"`
}
