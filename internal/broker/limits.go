package broker

import "time"

// allow counts an action of a nick against a per-minute limit. The counters live in the broker,
// not in the member, so leaving and joining again doesn't reset them. An allowed attempt is
// counted when it starts: one that then fails validation still uses its quota.
func (b *Broker) allow(ch *chat, nick, class string, limit int) bool {
	key := ch.id + "/" + nick + "/" + class
	now := time.Now()
	kept := recent(b.hits[key], now)
	if len(kept) >= limit {
		b.hits[key] = kept
		return false
	}
	b.hits[key] = append(kept, now)
	return true
}

func recent(ts []time.Time, now time.Time) []time.Time {
	kept := ts[:0]
	for _, t := range ts {
		if now.Sub(t) < time.Minute {
			kept = append(kept, t)
		}
	}
	return kept
}

// pruneHits drops counters with nothing recent left (the janitor calls it).
func (b *Broker) pruneHits(now time.Time) {
	for k, ts := range b.hits {
		if kept := recent(ts, now); len(kept) == 0 {
			delete(b.hits, k)
		} else {
			b.hits[k] = kept
		}
	}
}
