package peer

import (
	"fmt"
	"sync"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// Quarantine (docs/design-trust.md section 5): the new events of a source are not taken, directly or
// through a neighbor; a neighbor in quarantine has its session closed and no new one opened. What was
// taken stays. It begins by the owner's decision or automatically (a breach of the limits, a fork) and
// is lifted only by the owner.

type quarantined struct {
	mu sync.Mutex
	by map[string]store.QuarantineInfo
}

func (m *Manager) loadQuarantine() {
	lim, ok := m.st.(store.Limits)
	if !ok {
		return
	}
	list, err := lim.Quarantined()
	if err != nil {
		m.logf("peer: quarantine: %v", err)
		return
	}
	m.q.mu.Lock()
	defer m.q.mu.Unlock()
	m.q.by = map[string]store.QuarantineInfo{}
	for _, q := range list {
		m.q.by[q.Origin] = q
	}
}

// InQuarantine says whether a source is in quarantine, and why.
func (m *Manager) InQuarantine(origin string) (store.QuarantineInfo, bool) {
	m.q.mu.Lock()
	defer m.q.mu.Unlock()
	q, ok := m.q.by[origin]
	return q, ok
}

// Quarantine puts a source in quarantine and tells the broker (loudly) if it was not in already.
func (m *Manager) Quarantine(info store.QuarantineInfo) error {
	lim, ok := m.st.(store.Limits)
	if !ok {
		return fmt.Errorf("this store keeps no quarantine")
	}
	fresh, err := lim.Quarantine(info)
	if err != nil {
		return err
	}
	m.loadQuarantine()
	m.dropOrigin(info.Origin) // a neighbor in quarantine is let go
	if fresh && m.OnQuarantine != nil {
		m.OnQuarantine(info, false)
	}
	return m.Refresh()
}

// Release lifts a quarantine (the owner's decision); false if the source was not in.
func (m *Manager) Release(origin string) (bool, error) {
	lim, ok := m.st.(store.Limits)
	if !ok {
		return false, fmt.Errorf("this store keeps no quarantine")
	}
	info, _ := m.InQuarantine(origin)
	was, err := lim.ReleaseSource(origin)
	if err != nil {
		return false, err
	}
	m.sl.mu.Lock()
	delete(m.sl.src, origin) // the breaches of today are cleared in the store too
	m.sl.mu.Unlock()
	m.loadQuarantine()
	if was && m.OnQuarantine != nil {
		info.Origin = origin
		m.OnQuarantine(info, true)
	}
	return was, m.Refresh()
}

func (m *Manager) quarantinedNode(origin string) bool {
	_, ok := m.InQuarantine(origin)
	return ok
}

// autoQuarantine puts a source in quarantine after a breach that goes on or comes back.
func (m *Manager) autoQuarantine(origin, via, reason string) {
	info := store.QuarantineInfo{Origin: origin, Reason: reason, Since: time.Now().UTC().Format(time.RFC3339), By: "auto"}
	if via != origin {
		info.Via = via
	}
	if err := m.Quarantine(info); err != nil {
		m.logf("peer: quarantine of %s: %v", origin, err)
	}
}

// QuarantinedList lists the sources in quarantine.
func (m *Manager) QuarantinedList() []store.QuarantineInfo {
	m.q.mu.Lock()
	defer m.q.mu.Unlock()
	out := make([]store.QuarantineInfo, 0, len(m.q.by))
	for _, q := range m.q.by {
		out = append(out, q)
	}
	return out
}
