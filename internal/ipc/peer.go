package ipc

import (
	"errors"
	"path/filepath"
	"strconv"
	"time"
)

// Peer is what the kernel says about the process at the other end of a connection of the local
// endpoint. It can't be forged by the peer, but it is evidence for the log and for a person, not a
// border: another process of the same user can run the same binary (docs/design-keys.md section 8).
type Peer struct {
	PID     int
	Image   string    // the executable's path ("" when it can't be read)
	Started time.Time // when the process began (zero when unknown)
}

// ErrNoPeer: this connection (a test pipe, an unsupported system) can't tell who is at the other end.
var ErrNoPeer = errors.New("the peer of this connection is unknown")

// String is for a log line: the executable's name and the pid, never the full path.
func (p Peer) String() string {
	if p.PID == 0 {
		return "unknown"
	}
	name := "?"
	if p.Image != "" {
		name = filepath.Base(p.Image)
	}
	s := name + " pid " + strconv.Itoa(p.PID)
	if !p.Started.IsZero() {
		s += " started " + p.Started.UTC().Format("15:04:05")
	}
	return s
}
