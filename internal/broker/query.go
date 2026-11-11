package broker

import (
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"sort"
	"strings"
)

func (ch *chat) history(m *member, a proto.HistoryArgs) (any, *proto.Error) {
	limit := a.Limit
	if limit <= 0 {
		limit = historyDflt
	}
	if limit > historyMax {
		limit = historyMax
	}
	var src []proto.Message
	if a.Room == proto.RoomDirect {
		if a.With == "" {
			return nil, proto.Errf("member_required", "@direct history needs a member")
		}
		src = ch.dms[pairKey(m.name, a.With)]
	} else {
		r := ch.rooms[a.Room]
		if r == nil {
			return nil, proto.Errf("no_such_room", "no room @%s", a.Room)
		}
		if !r.members[m.name] {
			return nil, proto.Errf("not_in_room", "you are not in @%s", a.Room)
		}
		src = r.hist
	}
	if a.Before > 0 {
		i := sort.Search(len(src), func(i int) bool { return src[i].ID >= a.Before })
		src = src[:i]
	}
	return proto.HistoryResult{Messages: tail(src, limit)}, nil
}

// thread returns a thread's messages if m may see it, with a display label.
func (ch *chat) thread(m *member, key string) ([]proto.Message, string, bool) {
	switch {
	case strings.HasPrefix(key, "r:"):
		r := ch.rooms[key[2:]]
		if r == nil || !r.members[m.name] {
			return nil, "", false
		}
		return r.hist, r.name, true
	case strings.HasPrefix(key, "d:"):
		a, b, _ := strings.Cut(key[2:], "|")
		if m.name != a && m.name != b {
			return nil, "", false
		}
		peer := a
		if peer == m.name {
			peer = b
		}
		return ch.dms[key[2:]], proto.RoomDirect + "/@" + peer, true
	}
	return nil, "", false
}

// visibleThreads: keys of rooms m is in and m's own dialogs.
func (ch *chat) visibleThreads(m *member) []string { return ch.threadsOf(m.name) }

func (ch *chat) threadsOf(nick string) []string {
	var keys []string
	for name, r := range ch.rooms {
		if r.members[nick] {
			keys = append(keys, "r:"+name)
		}
	}
	for k := range ch.dms {
		a, b, _ := strings.Cut(k, "|")
		if a == nick || b == nick {
			keys = append(keys, "d:"+k)
		}
	}
	return keys
}

// threadMsgs: raw messages of a thread, no visibility check.
func (ch *chat) threadMsgs(key string) []proto.Message {
	switch {
	case strings.HasPrefix(key, "r:"):
		if r := ch.rooms[key[2:]]; r != nil {
			return r.hist
		}
	case strings.HasPrefix(key, "d:"):
		return ch.dms[key[2:]]
	}
	return nil
}

// timeline: everything m can see that was sent by or to a nick (default: m), oldest first.
func (ch *chat) timeline(m *member, a proto.TimelineArgs) (any, *proto.Error) {
	who := a.Member
	if who == "" {
		who = m.name
	}
	limit := a.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > historyMax {
		limit = historyMax
	}
	var out []proto.Message
	for _, k := range ch.visibleThreads(m) {
		for _, msg := range ch.threadMsgs(k) {
			if (msg.From == who || msg.To == who) && (a.Before <= 0 || msg.ID < a.Before) {
				out = append(out, msg)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return proto.HistoryResult{Messages: tail(out, limit)}, nil
}

func (ch *chat) search(m *member, a proto.SearchArgs) (any, *proto.Error) {
	terms := strings.Fields(strings.ToLower(a.Query))
	if len(terms) == 0 && a.From == "" {
		return nil, proto.Errf("bad_args", "query or from is required")
	}
	limit := a.Limit
	if limit <= 0 {
		limit = historyDflt
	}
	if limit > 100 {
		limit = 100
	}
	var keys []string
	switch {
	case a.Room == proto.RoomDirect:
		if a.With == "" {
			return nil, proto.Errf("member_required", "@direct search needs a member")
		}
		keys = []string{"d:" + pairKey(m.name, a.With)}
	case a.Room != "":
		keys = []string{"r:" + a.Room}
	default:
		keys = ch.visibleThreads(m)
	}
	var hits []proto.Message
	for _, k := range keys {
		msgs, _, ok := ch.thread(m, k)
		if !ok {
			if a.Room != "" && a.Room != proto.RoomDirect {
				return nil, proto.Errf("not_in_room", "you are not in @%s", a.Room)
			}
			continue
		}
		for _, msg := range msgs {
			if a.From != "" && msg.From != a.From {
				continue
			}
			text := strings.ToLower(msg.Text)
			match := true
			for _, t := range terms {
				if !strings.Contains(text, t) {
					match = false
					break
				}
			}
			if match {
				hits = append(hits, msg)
			}
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].ID > hits[j].ID })
	total := len(hits)
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return proto.SearchResult{Messages: hits, Total: total}, nil
}

func (ch *chat) context(m *member, a proto.ContextArgs) (any, *proto.Error) {
	if len(a.IDs) == 0 {
		return nil, proto.Errf("bad_args", "ids is required")
	}
	if len(a.IDs) > 50 {
		return nil, proto.Errf("bad_args", "at most 50 ids")
	}
	radius := a.Radius
	if radius <= 0 {
		radius = 3
	}
	if radius > 50 {
		radius = 50
	}
	type span struct {
		lo, hi int
		hits   []int64
	}
	byThread := map[string][]span{}
	labels := map[string]string{}
	var order []string
	var res proto.ContextResult
	for _, id := range a.IDs {
		key, ok := ch.where[id]
		var msgs []proto.Message
		var label string
		if ok {
			msgs, label, ok = ch.thread(m, key)
		}
		if !ok {
			res.Missing = append(res.Missing, id) // unknown and not-visible look the same
			continue
		}
		i := sort.Search(len(msgs), func(i int) bool { return msgs[i].ID >= id })
		if i == len(msgs) || msgs[i].ID != id {
			res.Missing = append(res.Missing, id)
			continue
		}
		if _, seen := byThread[key]; !seen {
			order = append(order, key)
		}
		labels[key] = label
		byThread[key] = append(byThread[key], span{max(0, i-radius), min(len(msgs)-1, i+radius), []int64{id}})
	}
	for _, key := range order {
		msgs, _, _ := ch.thread(m, key)
		spans := byThread[key]
		sort.Slice(spans, func(i, j int) bool { return spans[i].lo < spans[j].lo })
		merged := []span{spans[0]}
		for _, s := range spans[1:] {
			last := &merged[len(merged)-1]
			if s.lo <= last.hi+1 {
				last.hi = max(last.hi, s.hi)
				last.hits = append(last.hits, s.hits...)
				continue
			}
			merged = append(merged, s)
		}
		for _, s := range merged {
			sort.Slice(s.hits, func(i, j int) bool { return s.hits[i] < s.hits[j] })
			res.Blocks = append(res.Blocks, proto.ContextBlock{
				Thread:   labels[key],
				Hits:     s.hits,
				Messages: append([]proto.Message(nil), msgs[s.lo:s.hi+1]...),
			})
		}
	}
	return res, nil
}
