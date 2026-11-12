//go:build !windows && !linux && !darwin

package ipc

import "net"

// PeerOf: this system can't tell who is at the other end.
func PeerOf(net.Conn) (Peer, error) { return Peer{}, ErrNoPeer }
