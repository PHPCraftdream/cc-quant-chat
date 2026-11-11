package broker

import (
	"strconv"

	"github.com/PHPCraftdream/cc-quant-chat/internal/peer"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// The limits per source of the exchange with other nodes (docs/design-trust.md section 4) are
// settings of the owner; a change applies at once.

type peerLimitKey struct {
	name string
	def  int64
	max  int64
	help string
}

var peerLimitKeys = []peerLimitKey{
	{"peer.limit.events_per_minute", int64(peer.DefaultLimits.EventsPerMin), 1_000_000, "events of one source taken in a minute; more are refused, and a breach that goes on puts the source in quarantine (whole number)"},
	{"peer.limit.mib_per_day", peer.DefaultLimits.BytesPerDay >> 20, 1 << 20, "MiB of events of one source taken in a day (whole number)"},
	{"peer.limit.lamport_per_hour", peer.DefaultLimits.LamportPerHour, 1 << 40, "how far, summed over an hour, the clock of the events of a source may run ahead of this node's (whole number)"},
	{"peer.limit.sources", int64(peer.DefaultLimits.Sources), 10_000, "sources that are not neighbors here that one trusted neighbor may bring (whole number)"},
}

func init() {
	for _, k := range peerLimitKeys {
		configKeys = append(configKeys, configKey{name: k.name, help: k.help})
	}
}

func findPeerLimitKey(name string) (peerLimitKey, bool) {
	for _, k := range peerLimitKeys {
		if k.name == name {
			return k, true
		}
	}
	return peerLimitKey{}, false
}

func validatePeerLimit(k peerLimitKey, value string) (string, *proto.Error) {
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 1 || n > k.max {
		return "", proto.Errf("bad_args", "%s wants a whole number 1..%d, got %q", k.name, k.max, value)
	}
	return strconv.FormatInt(n, 10), nil
}

// peerLimitValue is the effective value of a limit: the setting, else the default.
func (b *Broker) peerLimitValue(k peerLimitKey) int64 {
	if v, ok := b.setting("", k.name); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return k.def
}

// applyPeerLimits gives the exchange layer the limits as they are now.
func (b *Broker) applyPeerLimits() {
	if b.peerMgr == nil {
		return
	}
	v := func(i int) int64 { return b.peerLimitValue(peerLimitKeys[i]) }
	b.peerMgr.SetLimits(peer.Limits{EventsPerMin: int(v(0)), BytesPerDay: v(1) << 20, LamportPerHour: v(2), Sources: int(v(3))})
}
