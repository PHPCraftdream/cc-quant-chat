package peer

import (
	"fmt"
	"sync"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// Limits per source (docs/design-trust.md section 4). A source is the origin of the events, whoever
// brought them. A breach refuses the event without ending the session; a breach that goes on, or comes
// back, puts the source in quarantine.
type Limits struct {
	EventsPerMin   int   // events of one source in a minute
	BytesPerDay    int64 // bytes of one source in a day (UTC)
	LamportPerHour int64 // the excess of the clock of its events over ours, summed over an hour
	Sources        int   // sources that are not neighbors that one trusted neighbor may bring
}

// DefaultLimits are used until the broker sets its own.
var DefaultLimits = Limits{EventsPerMin: 300, BytesPerDay: 50 << 20, LamportPerHour: 1000, Sources: maxSourcesVia}

const (
	episodeGap     = time.Minute     // breaches closer than this are one episode
	sustainedFor   = 5 * time.Minute // an episode this long is a sustained breach
	violationsADay = 3               // episodes in a day that make a quarantine
)

// SetLimits changes the limits; it applies to what comes next.
func (m *Manager) SetLimits(l Limits) {
	m.limMu.Lock()
	m.lim = l
	m.limMu.Unlock()
}

func (m *Manager) limits() Limits {
	m.limMu.Lock()
	defer m.limMu.Unlock()
	return m.lim
}

// srcState is the counters of one source: what the store holds of today (base) plus what has not been
// written yet (delta), and the windows that live in memory only.
type srcState struct {
	day         string
	base, delta store.DayStats
	minute      int64 // the unix minute counted in inMinute
	inMinute    int
	hour        int64 // the unix hour counted in excess
	excess      int64
	episodeFrom time.Time // the first breach of the episode that is going on
	lastBreach  time.Time
}

// sourceLimits holds the counters of every source seen since the broker started.
type sourceLimits struct {
	mu  sync.Mutex
	src map[string]*srcState
}

func (m *Manager) stateOf(origin string, now time.Time) *srcState {
	day := now.UTC().Format("2006-01-02")
	st := m.sl.src[origin]
	if st == nil || st.day != day {
		if st != nil {
			m.flushState(origin, st)
		}
		st = &srcState{day: day}
		if lim, ok := m.st.(store.Limits); ok {
			if ds, err := lim.DayStats(origin, day); err == nil {
				st.base = ds
			}
		}
		m.sl.src[origin] = st
	}
	return st
}

func (m *Manager) flushState(origin string, st *srcState) {
	if st.delta == (store.DayStats{}) {
		return
	}
	if lim, ok := m.st.(store.Limits); ok {
		if err := lim.AddDayStats(origin, st.day, st.delta); err != nil {
			m.logf("peer: counters of %s: %v", origin, err)
			return
		}
	}
	st.base.Events += st.delta.Events
	st.base.Bytes += st.delta.Bytes
	st.base.Violations += st.delta.Violations
	st.delta = store.DayStats{}
}

// flushLimits writes the counters that have not been written.
func (m *Manager) flushLimits() {
	m.sl.mu.Lock()
	defer m.sl.mu.Unlock()
	for origin, st := range m.sl.src {
		m.flushState(origin, st)
	}
}

// eventSize is what an event counts as against the bytes of a day.
func eventSize(e events.Event) int { return len(e.Payload) + 256 }

func lamportExcess(e events.Event, clock int64) int64 {
	return max(0, e.Lamport-(clock+1))
}

// overLimit says which limit taking the event would break ("" if none). clock is the highest clock
// value of the chat here. A breach is recorded; quarantine is non-empty if it makes one.
func (m *Manager) overLimit(e events.Event, clock int64, now time.Time) (why, quarantine string) {
	l := m.limits()
	m.sl.mu.Lock()
	defer m.sl.mu.Unlock()
	st := m.stateOf(e.Origin, now)
	if mi := now.Unix() / 60; mi != st.minute {
		st.minute, st.inMinute = mi, 0
	}
	if hr := now.Unix() / 3600; hr != st.hour {
		st.hour, st.excess = hr, 0
	}
	switch {
	case l.EventsPerMin > 0 && st.inMinute+1 > l.EventsPerMin:
		why = fmt.Sprintf("more than %d events a minute", l.EventsPerMin)
	case l.BytesPerDay > 0 && st.base.Bytes+st.delta.Bytes+int64(eventSize(e)) > l.BytesPerDay:
		why = fmt.Sprintf("more than %d MiB a day", l.BytesPerDay>>20)
	case l.LamportPerHour > 0 && st.excess+lamportExcess(e, clock) > l.LamportPerHour:
		why = fmt.Sprintf("its clock runs ahead of ours by more than %d in an hour", l.LamportPerHour)
	default:
		return "", ""
	}
	if st.lastBreach.IsZero() || now.Sub(st.lastBreach) > episodeGap { // a new episode
		st.episodeFrom = now
		st.delta.Violations++
		m.flushState(e.Origin, st)
	}
	st.lastBreach = now
	switch {
	case st.base.Violations+st.delta.Violations >= violationsADay:
		return why, fmt.Sprintf("%s: the limits were breached %d times today", why, violationsADay)
	case now.Sub(st.episodeFrom) >= sustainedFor:
		return why, fmt.Sprintf("%s: it goes on for more than %v", why, sustainedFor)
	}
	return why, ""
}

// took counts an event that was taken.
func (m *Manager) took(e events.Event, clock int64, now time.Time) {
	m.sl.mu.Lock()
	defer m.sl.mu.Unlock()
	st := m.stateOf(e.Origin, now)
	st.inMinute++
	st.delta.Events++
	st.delta.Bytes += int64(eventSize(e))
	st.excess += lamportExcess(e, clock)
}

// DayStats is what is recorded of a source today.
func (m *Manager) DayStats(origin string) store.DayStats { return m.DayStatsAt(origin, m.now()) }

// now is the time the limits count by (see clock).
func (m *Manager) now() time.Time {
	if m.clock != nil {
		return m.clock()
	}
	return time.Now()
}

// DayStatsAt is DayStats for the day of the given time.
func (m *Manager) DayStatsAt(origin string, now time.Time) store.DayStats {
	m.sl.mu.Lock()
	defer m.sl.mu.Unlock()
	st := m.stateOf(origin, now)
	return store.DayStats{Events: st.base.Events + st.delta.Events, Bytes: st.base.Bytes + st.delta.Bytes, Violations: st.base.Violations + st.delta.Violations}
}

// Limits returns the limits as they are now.
func (m *Manager) Limits() Limits { return m.limits() }
