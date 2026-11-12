package broker

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// Settings the owner can change with `ccqc config set` (an administrator operation). Only what
// is really worth changing: not a registry of every constant. The value is read when it is used,
// so a change applies without a restart; environment variables stay the default.
type configKey struct {
	name string
	chat bool // scoped to a chat
	help string
}

var configKeys = []configKey{
	{"broker.idle", false, "stop the broker after this long without connections (duration, e.g. 30m)"},
	{"broker.grace", false, "how long a disconnected member stays present (duration, e.g. 60s)"},
	{"limits.messages_per_minute", false, "messages per minute per nick (whole number)"},
	{"peer.advertise", false, "the address other nodes reach this broker at (host:port): what a node invitation carries; empty: give --addr to `ccqc peer invite`"},
	{"peer.listen", false, "where this broker accepts other nodes of the exchange of chats, host:port; empty: it accepts no one (applies at the next start of the broker)"},
	{"keys.bind_session", false, "on (default): a protected nick enrolled by a session is given its key only to that session (or its continuation after /clear or a compaction); off: another session is let in, and the chat is told (an emergency switch)"},
	{"keys.require_protected", false, "off (default): nicks with a plain key still join as before; on: only protected keys join (a plain one is moved by `ccqc keys migrate` or the next join of a new client; older clients are refused); turned on by the owner when every client is updated"},
	{"board.manager_role", true, "pinned role that may move tasks to other states, start/close sprints, change the process and revert; empty: everyone"},
}

func configNames() string {
	names := make([]string, len(configKeys))
	for i, k := range configKeys {
		names[i] = k.name
	}
	return strings.Join(names, ", ")
}

func findKey(name string) (configKey, bool) {
	for _, k := range configKeys {
		if k.name == name {
			return k, true
		}
	}
	return configKey{}, false
}

// setting returns a stored value ("" scope: broker-wide).
func (b *Broker) setting(scope, key string) (string, bool) {
	b.setMu.RLock()
	defer b.setMu.RUnlock()
	v, ok := b.settings[scope][key]
	return v, ok
}

func (b *Broker) idleFor() time.Duration {
	if v, ok := b.setting("", "broker.idle"); ok {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return b.idle
}

func (b *Broker) graceFor() time.Duration {
	if v, ok := b.setting("", "broker.grace"); ok {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return b.grace
}

func (b *Broker) msgLimit() int {
	if v, ok := b.setting("", "limits.messages_per_minute"); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return rateLimit
}

// managerRole is the pinned role that manages the board of a chat ("" = nobody in particular).
func (b *Broker) managerRole(chatID string) string {
	v, _ := b.setting(chatID, "board.manager_role")
	return v
}

// loadSettings reads the stored settings at start.
func (b *Broker) loadSettings() error {
	b.setMu.Lock()
	defer b.setMu.Unlock()
	b.settings = map[string]map[string]string{}
	as, ok := b.store.(store.AdminStore)
	if !ok {
		return nil
	}
	all, err := as.Settings()
	if err != nil {
		return err
	}
	b.settings = all
	return nil
}

// validate checks a value for a key and returns it normalized.
func validateSetting(k configKey, value string) (string, *proto.Error) {
	value = strings.TrimSpace(value)
	if lk, ok := findPeerLimitKey(k.name); ok {
		return validatePeerLimit(lk, value)
	}
	switch k.name {
	case "broker.idle", "broker.grace":
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return "", proto.Errf("bad_args", "%s wants a positive duration like 30m or 60s, got %q", k.name, value)
		}
		return d.String(), nil
	case "limits.messages_per_minute":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 10000 {
			return "", proto.Errf("bad_args", "%s wants a whole number 1..10000, got %q", k.name, value)
		}
		return strconv.Itoa(n), nil
	case "peer.listen", "peer.advertise":
		if value != "" {
			if _, _, err := net.SplitHostPort(value); err != nil {
				return "", proto.Errf("bad_args", "%s wants host:port or empty, got %q", k.name, value)
			}
		}
		return value, nil
	case "keys.bind_session", "keys.require_protected":
		if value != "on" && value != "off" {
			return "", proto.Errf("bad_args", "%s wants on or off, got %q", k.name, value)
		}
		return value, nil
	case "board.manager_role":
		if value != "" && !proto.ValidName(value) {
			return "", proto.Errf("bad_args", "%s wants a role name [a-z0-9-]{1,32} or empty, got %q", k.name, value)
		}
		return value, nil
	}
	return value, nil
}

// setConfig validates, stores and applies a setting.
func (b *Broker) setConfig(chatID, key, value string, at *store.AdminAttest) (string, *proto.Error) {
	k, ok := findKey(key)
	if !ok {
		return "", proto.Errf("bad_args", "unknown setting %q; known: %s", key, configNames())
	}
	scope := ""
	if k.chat {
		if chatID == "" {
			return "", proto.Errf("bad_args", "%s is a per-chat setting: name the chat", key)
		}
		scope = chatID
	}
	v, e := validateSetting(k, value)
	if e != nil {
		return "", e
	}
	if cs, ok := b.store.(store.AttestedSettings); ok && scope != "" && at != nil {
		if err := cs.SetChatSettingAttested(scope, key, v, *at); err != nil {
			return "", proto.Errf("store_failed", "%v", err)
		}
	} else if as, ok := b.store.(store.AdminStore); ok {
		if err := as.SetSetting(scope, key, v); err != nil {
			return "", proto.Errf("store_failed", "%v", err)
		}
	}
	b.setMu.Lock()
	if b.settings == nil {
		b.settings = map[string]map[string]string{}
	}
	if b.settings[scope] == nil {
		b.settings[scope] = map[string]string{}
	}
	b.settings[scope][key] = v
	b.setMu.Unlock()
	if _, ok := findPeerLimitKey(key); ok {
		b.applyPeerLimits()
	}
	return v, nil
}

// configList shows every setting with its effective value and where it comes from.
func (b *Broker) configList(chatID string) proto.ConfigList {
	var out proto.ConfigList
	for _, k := range configKeys {
		it := proto.ConfigItem{Key: k.name, Chat: k.chat, Help: k.help}
		scope := ""
		if k.chat {
			scope = chatID
		}
		switch k.name {
		case "broker.idle":
			it.Default, it.Value, it.Source = fmtDur(b.idle), fmtDur(b.idleFor()), b.source(scope, k.name, envSet("CCQC_BROKER_IDLE"))
		case "broker.grace":
			it.Default, it.Value, it.Source = fmtDur(b.grace), fmtDur(b.graceFor()), b.source(scope, k.name, envSet("CCQC_GRACE"))
		case "limits.messages_per_minute":
			it.Default, it.Value, it.Source = strconv.Itoa(rateLimit), strconv.Itoa(b.msgLimit()), b.source(scope, k.name, false)
		case "peer.listen", "peer.advertise":
			v, _ := b.setting("", k.name)
			it.Default, it.Value, it.Source = "", v, b.source(scope, k.name, false)
		case "keys.bind_session":
			it.Default, it.Value, it.Source = "on", "on", b.source(scope, k.name, false)
			if !b.bindSession() {
				it.Value = "off"
			}
		case "keys.require_protected":
			it.Default, it.Value, it.Source = "off", "off", b.source(scope, k.name, false)
			if b.requireProtected() {
				it.Value = "on"
			}
		case "board.manager_role":
			it.Default, it.Value, it.Source = "", b.managerRole(scope), b.source(scope, k.name, false)
		default:
			if lk, ok := findPeerLimitKey(k.name); ok {
				it.Default, it.Value, it.Source = strconv.FormatInt(lk.def, 10), strconv.FormatInt(b.peerLimitValue(lk), 10), b.source(scope, k.name, false)
			}
		}
		out.Items = append(out.Items, it)
	}
	return out
}

func (b *Broker) source(scope, key string, env bool) string {
	if _, ok := b.setting(scope, key); ok {
		return "setting"
	}
	if env {
		return "env"
	}
	return "default"
}

func fmtDur(d time.Duration) string { return d.String() }

func (b *Broker) describeConfig(chatID, key string) string {
	for _, it := range b.configList(chatID).Items {
		if it.Key == key {
			return fmt.Sprintf("%s = %s", it.Key, it.Value)
		}
	}
	return key
}

func envSet(k string) bool { return os.Getenv(k) != "" }
