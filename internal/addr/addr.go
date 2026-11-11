// Package addr parses chat addresses: /<chat>/@<room>[/@<member>].
package addr

import (
	"fmt"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

type Addr struct {
	Chat   string
	Room   string
	Member string
}

func (a Addr) String() string {
	s := "/" + a.Chat + "/@" + a.Room
	if a.Member != "" {
		s += "/@" + a.Member
	}
	return s
}

func Parse(s string) (Addr, error) {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'`")
	s = strings.TrimRight(s, "/")
	const want = "want /<chat>/@<room>[/@<member>], e.g. /vpn-dev/@common or /vpn-dev/@direct/@admin"
	if !strings.HasPrefix(s, "/") {
		return Addr{}, fmt.Errorf("address %q: %s", s, want)
	}
	parts := strings.Split(s[1:], "/")
	if len(parts) < 2 || len(parts) > 3 {
		return Addr{}, fmt.Errorf("address %q: %s", s, want)
	}
	a := Addr{Chat: parts[0]}
	if !proto.ValidName(a.Chat) {
		return Addr{}, fmt.Errorf("address %q: bad chat name; %s", s, want)
	}
	var ok bool
	if a.Room, ok = at(parts[1]); !ok {
		return Addr{}, fmt.Errorf("address %q: bad room part; %s", s, want)
	}
	if len(parts) == 3 {
		if a.Member, ok = at(parts[2]); !ok {
			return Addr{}, fmt.Errorf("address %q: bad member part; %s", s, want)
		}
	}
	if a.Room == proto.RoomDirect && a.Member == "" {
		return Addr{}, fmt.Errorf("address %q: @direct needs a member: /%s/@direct/@<member>", s, a.Chat)
	}
	return a, nil
}

func at(p string) (string, bool) {
	if !strings.HasPrefix(p, "@") {
		return "", false
	}
	n := p[1:]
	return n, proto.ValidName(n)
}

// Chat extracts a chat name from "vpn-dev", "/vpn-dev" or a full address.
func Chat(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'`")
	s = strings.TrimPrefix(s, "/")
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	return s
}
