package broker

import (
	"log"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// useChat finds the chat or loads it, and returns it locked, with a hold on it that keeps it from
// being unloaded. The broker's lock is taken only to find the chat in the map or to put a new one
// there; the chat's own lock is waited for (and a chat is loaded) without it, so a chat that is slow
// or stuck holds up only those who need that chat. A chat that is being loaded is in the map locked:
// whoever else wants it waits on that lock. If it was unloaded while waited for, the search starts
// again. Pair it with doneChat.
func (b *Broker) useChat(id, name string) (*chat, error) {
	for {
		b.mu.Lock()
		ch := b.chats[id]
		if ch == nil {
			ch = newChat(id, name)
			ch.store = b.store
			ch.refs.Add(1)
			ch.mu.Lock() // the placeholder: locked until the chat is loaded
			b.chats[id] = ch
			b.mu.Unlock()
			if err := b.fillChat(ch); err != nil {
				ch.dead = true
				b.mu.Lock()
				if b.chats[id] == ch {
					delete(b.chats, id)
				}
				b.mu.Unlock()
				ch.refs.Add(-1)
				ch.mu.Unlock()
				return nil, err
			}
			return ch, nil
		}
		ch.refs.Add(1)
		b.mu.Unlock()
		ch.mu.Lock()
		if ch.dead { // unloaded while we waited: look again
			ch.mu.Unlock()
			ch.refs.Add(-1)
			continue
		}
		if ch.name == "" && name != "" { // loaded by an event of another node, which does not know the name
			ch.name = name
		}
		return ch, nil
	}
}

// doneChat gives the chat back: its lock and the hold. The caller then decides whether to let the
// chat go (unloadIfEmpty).
func (b *Broker) doneChat(ch *chat) {
	ch.mu.Unlock()
	ch.refs.Add(-1)
}

// fillChat loads the history of a new chat from the store and replays it. A chat whose history
// can't be read is not served: working from a partial state would hand out nick keys that already
// exist and reuse message ids.
func (b *Broker) fillChat(ch *chat) error {
	recs, err := b.store.Load(ch.id, ch.name)
	if err != nil {
		log.Printf("broker: chat %s#%s: load: %v", ch.name, ch.id[:8], err)
		return err
	}
	for _, r := range recs {
		ch.replay(r)
	}
	// The settings of a chat are kept with the chat.
	if cs, ok := b.store.(store.ChatSettingsStore); ok {
		m, err := cs.ChatSettings(ch.id)
		if err != nil {
			log.Printf("broker: chat %s#%s: settings: %v", ch.name, ch.id[:8], err)
			return err
		}
		if len(m) > 0 {
			b.setMu.Lock()
			if b.settings == nil {
				b.settings = map[string]map[string]string{}
			}
			b.settings[ch.id] = m
			b.setMu.Unlock()
		}
	}
	return nil
}

// chatList is a snapshot of the loaded chats, for those who go through all of them without holding
// the broker's lock while they wait for each chat's own.
func (b *Broker) chatList() []*chat {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*chat, 0, len(b.chats))
	for _, ch := range b.chats {
		out = append(out, ch)
	}
	return out
}

// unloadIfEmpty lets go of a chat nobody is present in or using, for a caller that holds no lock.
// A chat that is busy is left: it is not empty, or will be looked at again by the janitor.
func (b *Broker) unloadIfEmpty(ch *chat) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !ch.mu.TryLock() {
		return
	}
	defer ch.mu.Unlock()
	b.unloadLocked(ch)
}

// unloadLocked: the caller holds the broker's lock and the chat's. A chat that is gone from the
// map is dead: nobody can reach it any more, so no operation can race with the release.
func (b *Broker) unloadLocked(ch *chat) {
	if ch.dead || len(ch.members) > 0 || ch.refs.Load() > 0 || b.chats[ch.id] != ch {
		return
	}
	if _, memoryOnly := ch.store.(store.Memory); memoryOnly { // memory-only: unloading would lose history
		return
	}
	ch.dead = true
	delete(b.chats, ch.id)
	if r, ok := ch.store.(store.Releaser); ok { // its database is closed until the chat is used again
		r.Release(ch.id)
	}
	log.Printf("broker: chat %s#%s unloaded (nobody present)", ch.name, ch.id[:8])
}

func errNoStore(err error) *proto.Error {
	return proto.Errf("store_unavailable", "chat history can't be read (%v)", err)
}
