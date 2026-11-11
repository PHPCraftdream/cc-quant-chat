package broker

import (
	"errors"
	"fmt"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// crossNodeDM is what a member gets who writes a direct message to a member of another node.
const crossNodeDM = "the member is on another node; direct messages between nodes are not supported yet"

// acceptRemote stores an event that came from another node and applies it to the chat, under the
// chat's lock like every other change of the chat: the store writes the journal and the
// projections in one transaction and says what changed, in the numbers of this node; the broker
// then brings its memory up and tells the members who are present, as it does for a message of its
// own.
func (b *Broker) acceptRemote(chatID string, e events.Event) error {
	fed, ok := b.store.(store.Federation)
	if !ok {
		return errors.New("the store cannot take events of other nodes")
	}
	ch, err := b.useChat(chatID, "")
	if err != nil {
		return err
	}
	recs, err := fed.AcceptEvent(chatID, e)
	if err == nil {
		b.takeRemote(ch, recs)
	}
	b.doneChat(ch)
	b.unloadIfEmpty(ch)
	return err
}

// takeRemote applies the records of an event of another node to the chat in memory. The caller
// holds the chat's lock.
func (b *Broker) takeRemote(ch *chat, recs []store.Record) {
	for _, r := range recs {
		switch r.T {
		case "nick":
			ch.nicks[r.Nick] = &nick{pub: r.Pub, seen: ch.nextID} // nothing is unread for a nick that is not here
			ch.rooms[proto.RoomCommon].members[r.Nick] = true
		case "nick_key":
			if n := ch.nicks[r.Nick]; n != nil {
				n.pub = r.Pub
			}
			// A key that its node replaced or took away no longer protects the nick: the members hear of it.
			verb := "replaced"
			if r.Pub == "" {
				verb = "reset"
			}
			b.presence(ch, nil, "", fmt.Sprintf("the key of %s was %s by its node", r.Nick, verb))
		case "room":
			if rm := ch.rooms[r.Room]; rm == nil {
				ch.replay(r)
			} else if r.Topic != "" || r.Nick != "" { // the creation that stands: it may replace a placeholder
				rm.topic, rm.createdBy = r.Topic, r.Nick
			}
		case "in", "out":
			ch.replay(r)
		case "msg":
			if r.Msg == nil {
				continue
			}
			ch.replay(r)
			rm := ch.rooms[r.Msg.Room]
			if rm == nil {
				continue
			}
			for name := range rm.members {
				b.deliver(ch, name, proto.Event{Kind: "msg", Msg: r.Msg, ToMe: name == r.Msg.To})
			}
		}
	}
}
