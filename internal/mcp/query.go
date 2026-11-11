package mcp

import (
	"fmt"
	"github.com/PHPCraftdream/cc-quant-chat/internal/addr"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"sort"
	"strconv"
	"strings"
)

func (s *Session) history(target string, limit int, before int64) (string, error) {
	a, err := addr.Parse(target)
	if err != nil {
		return "", err
	}
	cc, err := s.get(a.Chat)
	if err != nil {
		return "", err
	}
	ha := proto.HistoryArgs{Room: a.Room, Limit: limit, Before: before}
	if a.Room == proto.RoomDirect {
		ha.With = a.Member
	} else if a.Member != "" {
		return "", fmt.Errorf("history target is /%s/@<room> or /%s/@direct/@<member>", a.Chat, a.Chat)
	}
	var r proto.HistoryResult
	if err := cc.call("history", ha, &r); err != nil {
		return "", err
	}
	if len(r.Messages) == 0 {
		return "no messages in " + a.String(), nil
	}
	var b strings.Builder
	for i := range r.Messages {
		b.WriteString(fmtMsg(cc.local, &r.Messages[i]) + "\n")
	}
	if len(r.Messages) >= pageSize(limit, 20) {
		fmt.Fprintf(&b, "(older: chat_history before=%d)\n", r.Messages[0].ID)
	}
	return b.String(), nil
}

// timeline: everything you can see sent by or to a nick (default: you), oldest first.
func (s *Session) timeline(chat, member string, limit int, before int64) (string, error) {
	cc, err := s.get(chat)
	if err != nil {
		return "", err
	}
	member = strings.TrimPrefix(strings.TrimSpace(member), "@")
	var r proto.HistoryResult
	if err := cc.call("timeline", proto.TimelineArgs{Member: member, Limit: limit, Before: before}, &r); err != nil {
		return "", err
	}
	who := member
	if who == "" {
		who = cc.name
	}
	if len(r.Messages) == 0 {
		return fmt.Sprintf("no messages from or to @%s visible to you in /%s", who, cc.local), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "/%s — messages from or to @%s:\n", cc.local, who)
	for i := range r.Messages {
		b.WriteString(fmtMsg(cc.local, &r.Messages[i]) + "\n")
	}
	if len(r.Messages) >= pageSize(limit, 50) {
		fmt.Fprintf(&b, "(older: chat_timeline before=%d)\n", r.Messages[0].ID)
	}
	return b.String(), nil
}

// search: one chat, one thread (in=address) or all joined chats.
func (s *Session) search(query, chat, in, from string, limit int) (string, error) {
	sa := proto.SearchArgs{Query: query, From: strings.TrimPrefix(strings.TrimSpace(from), "@"), Limit: limit}
	var targets []*chatConn
	switch {
	case strings.TrimSpace(in) != "":
		a, err := addr.Parse(in)
		if err != nil {
			return "", err
		}
		cc, err := s.get(a.Chat)
		if err != nil {
			return "", err
		}
		sa.Room = a.Room
		if a.Room == proto.RoomDirect {
			sa.With = a.Member
		}
		targets = []*chatConn{cc}
	case strings.TrimSpace(chat) != "":
		cc, err := s.get(chat)
		if err != nil {
			return "", err
		}
		targets = []*chatConn{cc}
	default:
		s.mu.Lock()
		for _, cc := range s.chats {
			targets = append(targets, cc)
		}
		s.mu.Unlock()
		if len(targets) == 0 {
			return "", fmt.Errorf("not_joined: not in any chat")
		}
		sort.Slice(targets, func(i, j int) bool { return targets[i].local < targets[j].local })
	}
	var b strings.Builder
	found := 0
	for _, cc := range targets {
		var r proto.SearchResult
		if err := cc.call("search", sa, &r); err != nil {
			fmt.Fprintf(&b, "/%s: %v\n", cc.local, err)
			continue
		}
		if r.Total == 0 {
			continue
		}
		found += r.Total
		fmt.Fprintf(&b, "/%s: %d hit(s), showing %d, newest first\n", cc.local, r.Total, len(r.Messages))
		for i := range r.Messages {
			b.WriteString("  " + fmtMsg(cc.local, &r.Messages[i]) + "\n")
		}
	}
	if found == 0 && b.Len() == 0 {
		return "no matches", nil
	}
	b.WriteString("(surroundings: chat_context chat=<chat> ids=[...] radius=N)\n")
	return b.String(), nil
}

// context: messages within radius around each id, grouped by thread; overlapping windows merged.
func (s *Session) context(chat string, ids []int64, radius int) (string, error) {
	cc, err := s.get(chat)
	if err != nil {
		return "", err
	}
	var r proto.ContextResult
	if err := cc.call("context", proto.ContextArgs{IDs: ids, Radius: radius}, &r); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, blk := range r.Blocks {
		hit := set(nil)
		for _, h := range blk.Hits {
			hit[strconv.FormatInt(h, 10)] = true
		}
		first, last := blk.Messages[0].ID, blk.Messages[len(blk.Messages)-1].ID
		fmt.Fprintf(&b, "── /%s/@%s  #%d…#%d\n", cc.local, blk.Thread, first, last)
		for i := range blk.Messages {
			mark := "   "
			if hit[strconv.FormatInt(blk.Messages[i].ID, 10)] {
				mark = " » "
			}
			b.WriteString(mark + fmtMsg(cc.local, &blk.Messages[i]) + "\n")
		}
	}
	if len(r.Missing) > 0 {
		miss := make([]string, len(r.Missing))
		for i, id := range r.Missing {
			miss[i] = "#" + strconv.FormatInt(id, 10)
		}
		fmt.Fprintf(&b, "not found or not visible to you: %s\n", strings.Join(miss, ", "))
	}
	return b.String(), nil
}

// pageSize mirrors the broker's default page size, to tell whether older messages may exist.
func pageSize(limit, dflt int) int {
	if limit <= 0 {
		return dflt
	}
	return limit
}
