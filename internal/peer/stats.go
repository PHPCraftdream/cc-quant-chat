package peer

// PeerStats counts, since this broker started, what was taken from a node and what was not.
type PeerStats struct {
	Accepted int
	Refused  map[string]int // by reason
}

func (m *Manager) noteAccepted(origin string) {
	m.statsMu.Lock()
	defer m.statsMu.Unlock()
	m.statsOf(origin).Accepted++
}

func (m *Manager) noteRefused(origin, why string) {
	m.statsMu.Lock()
	defer m.statsMu.Unlock()
	m.statsOf(origin).Refused[why]++
}

func (m *Manager) statsOf(origin string) *PeerStats {
	st := m.stats[origin]
	if st == nil {
		st = &PeerStats{Refused: map[string]int{}}
		m.stats[origin] = st
	}
	return st
}

// Stats returns a copy of the counters of a node.
func (m *Manager) Stats(origin string) PeerStats {
	m.statsMu.Lock()
	defer m.statsMu.Unlock()
	st := m.statsOf(origin)
	out := PeerStats{Accepted: st.Accepted, Refused: map[string]int{}}
	for k, v := range st.Refused {
		out.Refused[k] = v
	}
	return out
}
