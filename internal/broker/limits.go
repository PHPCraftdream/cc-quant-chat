package broker

import (
	"time"
)

// allow counts an action of a nick against a per-minute limit. The counters live in the broker,
// not in the member, so leaving and joining again doesn't reset them. An allowed attempt is
// counted when it starts: one that then fails validation still uses its quota.
func (b *Broker) allow(ch *chat, nick, class string, limit int) bool {
	b.hitsMu.Lock()
	defer b.hitsMu.Unlock()
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
	b.hitsMu.Lock()
	defer b.hitsMu.Unlock()
	for k, ts := range b.hits {
		if kept := recent(ts, now); len(kept) == 0 {
			delete(b.hits, k)
		} else {
			b.hits[k] = kept
		}
	}
}

// refund takes back the newest counted action (a send that turned out to be a repeat).
func (b *Broker) refund(ch *chat, nick, class string) {
	b.hitsMu.Lock()
	defer b.hitsMu.Unlock()
	key := ch.id + "/" + nick + "/" + class
	if ts := b.hits[key]; len(ts) > 0 {
		b.hits[key] = ts[:len(ts)-1]
	}
}

const sentCap = 4096 // remembered idempotent sends per chat

func (ch *chat) sentID(nick, cid string) (int64, bool) {
	if cid == "" {
		return 0, false
	}
	id, ok := ch.sent[nick+"|"+cid]
	return id, ok
}

// rememberSent keeps the newest sentCap client ids; older repeats are still caught by the store.
func (ch *chat) rememberSent(nick, cid string, id int64) {
	if cid == "" {
		return
	}
	k := nick + "|" + cid
	if _, ok := ch.sent[k]; !ok {
		ch.sentOrd = append(ch.sentOrd, k)
		if len(ch.sentOrd) > sentCap {
			delete(ch.sent, ch.sentOrd[0])
			ch.sentOrd = ch.sentOrd[1:]
		}
	}
	ch.sent[k] = id
}
