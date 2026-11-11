package peer

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// Events of the board and of members made by hand, as a node that lies would send them.

const stamp = "2026-01-01T00:00:00Z"

type row = map[string]any

func j(v any) string { b, _ := json.Marshal(v); return string(b) }

func taskRow(id int, title, status string) row {
	return row{"t": "board_tasks", "r": row{"id": id, "title": title, "description": "", "status": status, "sprint": 0, "assignee": "",
		"priority": 0, "created_by": "rogue", "created_at": stamp, "updated_at": stamp, "deleted": 0}}
}

func logRow(id int, kind, entity string, entityID int, before, after row, reverts int) row {
	return row{"t": "board_log", "r": row{"id": id, "ts": stamp, "actor": "rogue", "actor_owner": 0, "kind": kind, "entity": entity,
		"entity_id": entityID, "before_json": j(before), "after_json": j(after), "reverts": reverts, "note": "", "origin": "", "lamport": 0,
		"actor_grant": "", "author_pub": "", "sig": "", "payload": ""}}
}

func ref(origin string, seq int) row { return row{"o": origin, "s": seq} }

// boardEvent is the next event of the rogue's chain: a board operation with the images it claims.
func (r *rogue) boardEvent(seq int64, prev string, kind string, payload row, rows []row, refs row) events.Event {
	e := events.Event{ChatID: chatX, Origin: r.origin, Seq: seq, Prev: prev, Lamport: seq, Kind: kind, Payload: j(payload),
		Assigned: j(row{"actor": "rogue", "rows": rows, "refs": refs}), TS: time.Now().UTC().Format(time.RFC3339)}
	e.Sign(r.priv)
	return e
}

func authorSign(e events.Event, node ed25519.PrivateKey, priv ed25519.PrivateKey) events.Event {
	e.AuthorPub = base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
	e.Sig = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, proto.EventMessage(e.ChatID, e.Origin, e.Kind, []byte(e.Payload), e.Lamport)))
	e.Sign(node)
	return e
}

func newNickKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func (v *tnode) tasks() []store.Task {
	v.t.Helper()
	var out []store.Task
	err := v.db.View(chatX, func(tx store.BoardTx) error {
		var err error
		out, err = tx.ListTasks(store.TaskFilter{Sprint: -1})
		return err
	})
	if err != nil {
		v.t.Fatal(err)
	}
	return out
}

func createTask(r *rogue, seq int64, prev string) events.Event {
	return r.boardEvent(seq, prev, "task.create", row{"op": "task.create", "title": "x"},
		[]row{taskRow(1, "x", "todo"), logRow(1, "task.create", "task", 1, row{}, row{"title": "x", "description": "", "status": "todo", "sprint": 0, "assignee": "", "priority": 0, "deleted": false}, 0)}, nil)
}

func TestAMessageOfANickThatHasAKeyMustBeSignedByIt(t *testing.T) {
	r := newRogue(t)
	v := victim(t, r)
	key := newNickKey(t)
	register := r.event(chatX, 1, "", 1, "nick.register", j(row{"nick": "rogue", "pub": base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))}))
	r.send(chatX, nil, register)
	eventually(t, wait, "the registration", func() bool { return v.holds(chatX, register) })

	unsigned := r.event(chatX, 2, register.ID(), 2, "msg", msgPayload)
	unsigned.Assigned = `{"from":"rogue","id":1,"thread":"r:common","owner":true}` // "the owner said it" does not excuse the missing key
	unsigned.Sign(r.priv)
	r.send(chatX, nil, unsigned)
	eventually(t, wait, "the refusal", func() bool { return v.logged("must be signed with it") })
	if v.holds(chatX, unsigned) {
		t.Fatal("an unsigned message of a nick that has a key was kept")
	}

	good := authorSign(r.event(chatX, 2, register.ID(), 2, "msg", msgPayload), r.priv, key)
	r.send(chatX, nil, good)
	eventually(t, wait, "the signed message", func() bool { return v.holds(chatX, good) })

	other := authorSign(r.event(chatX, 3, good.ID(), 3, "msg", msgPayload), r.priv, newNickKey(t))
	r.send(chatX, nil, other)
	eventually(t, wait, "the key of another nick refused", func() bool { return v.logged("not the one registered") })
}

func TestABoardEventIsCheckedAgainstTheRequestTheAuthorSigned(t *testing.T) {
	r := newRogue(t)
	v := victim(t, r)
	key := newNickKey(t)
	register := r.event(chatX, 1, "", 1, "nick.register", j(row{"nick": "rogue", "pub": base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))}))
	// The nick that signs is the one the entries name as actor.
	create := authorSign(createTask(r, 2, register.ID()), r.priv, key)
	r.send(chatX, nil, register, create)
	eventually(t, wait, "the task", func() bool { return len(v.tasks()) == 1 })
	me := ref(r.origin, 2)
	task := row{"task": row{"1": me}}

	// An entry that sets a field the signed request does not carry.
	update := func(seq int64, prev string, after row) events.Event {
		return authorSign(r.boardEvent(seq, prev, "task.update", row{"op": "task.update", "id": 1, "title": "y"},
			[]row{taskRow(1, "y", "todo"), logRow(2, "task.update", "task", 1, row{"title": "x"}, after, 0)}, task), r.priv, key)
	}
	bad := update(3, create.ID(), row{"title": "y", "assignee": "mallory"})
	r.send(chatX, nil, bad)
	eventually(t, wait, "the refusal", func() bool { return v.logged("does not carry") })
	if v.holds(chatX, bad) || v.tasks()[0].Assignee != "" {
		t.Fatal("a node added a change in the name of the author")
	}
	good := update(3, create.ID(), row{"title": "y"})
	r.send(chatX, nil, good)
	eventually(t, wait, "the honest update", func() bool { return v.holds(chatX, good) && v.tasks()[0].Title == "y" })
}

func TestACommentCannotChangeTheTaskAndARevertOnlyUndoesItsEntry(t *testing.T) {
	r := newRogue(t)
	v := victim(t, r)
	create := createTask(r, 1, "")
	r.send(chatX, nil, create)
	eventually(t, wait, "the task", func() bool { return len(v.tasks()) == 1 })
	task := row{"task": row{"1": ref(r.origin, 1)}}

	// A comment whose entry also says the task is done.
	comment := func(after row) events.Event {
		return r.boardEvent(2, create.ID(), "task.comment", row{"op": "task.comment", "id": 1, "text": "hi"},
			[]row{{"t": "board_comments", "r": row{"id": 1, "task_id": 1, "ts": stamp, "author": "rogue", "author_owner": 0, "text": "hi", "deleted": 0, "origin": "", "lamport": 0, "author_pub": "", "sig": ""}},
				logRow(2, "task.comment", "task", 1, row{}, after, 0)}, task)
	}
	forged := comment(row{"comment": 1, "text": "hi", "hidden": false, "status": "done"})
	r.send(chatX, nil, forged)
	eventually(t, wait, "the forged comment refused", func() bool { return v.logged("may not set") })
	if v.tasks()[0].Status != "todo" {
		t.Fatal("a comment changed the task")
	}
	honest := comment(row{"comment": 1, "text": "hi", "hidden": false})
	r.send(chatX, nil, honest)
	eventually(t, wait, "the honest comment", func() bool { return v.holds(chatX, honest) })

	// An update, then reverts of it: one that sets what it should not, one that undoes it.
	update := r.boardEvent(3, honest.ID(), "task.update", row{"op": "task.update", "id": 1, "title": "z"},
		[]row{taskRow(1, "z", "todo"), logRow(3, "task.update", "task", 1, row{"title": "x"}, row{"title": "z"}, 0)}, task)
	r.send(chatX, nil, update)
	eventually(t, wait, "the update", func() bool { return v.holds(chatX, update) })
	named := row{"task": row{"1": ref(r.origin, 1)}, "log": row{"3": ref(r.origin, 3)}}
	revert := func(after row) events.Event {
		return r.boardEvent(4, update.ID(), "revert", row{"op": "revert", "log_id": 3},
			[]row{taskRow(1, "x", "todo"), logRow(4, "revert", "task", 1, row{"title": "z"}, after, 3)}, named)
	}
	wrong := revert(row{"title": "something else"})
	r.send(chatX, nil, wrong)
	eventually(t, wait, "the wrong revert refused", func() bool { return v.logged("something other than what it was") })
	if v.holds(chatX, wrong) || v.tasks()[0].Title != "z" {
		t.Fatal("a revert set what its entry never had")
	}
	right := revert(row{"title": "x"})
	r.send(chatX, nil, right)
	eventually(t, wait, "the revert", func() bool { return v.holds(chatX, right) && v.tasks()[0].Title == "x" })
}

func TestAnEventThatNamesWhatHasNotComeIsWaitedForQuietly(t *testing.T) {
	r, third := newRogue(t), newRogue(t)
	v := victim(t, r)
	made := createTask(third, 1, "")
	made.Assigned = j(row{"actor": "rogue", "rows": []row{taskRow(1, "x", "todo"), logRow(1, "task.create", "task", 1, row{}, row{"title": "x", "description": "", "status": "todo", "sprint": 0, "assignee": "", "priority": 0, "deleted": false}, 0)}})
	made.Sign(third.priv)
	pubs := map[string]string{third.origin: base64.StdEncoding.EncodeToString(third.pub)}
	move := r.boardEvent(1, "", "task.move", row{"op": "task.move", "id": 1, "status": "doing"},
		[]row{taskRow(1, "x", "doing"), logRow(1, "task.move", "task", 1, row{"status": "todo"}, row{"status": "doing"}, 0)},
		row{"task": row{"1": ref(third.origin, 1)}})

	move.Lamport = 5 // after the creation: a tie on the clock would be settled by the origins
	move.Sign(r.priv)
	r.send(chatX, nil, move) // the task it moves has not come
	time.Sleep(400 * time.Millisecond)
	if v.logged("was not kept") || v.holds(chatX, move) {
		t.Fatal("an event waiting for what it names was treated as refused")
	}
	r.send(chatX, pubs, made) // now it has
	eventually(t, wait, "the task", func() bool { return v.holds(chatX, made) })
	r.send(chatX, nil, move) // and the event comes again, as an honest peer would send it
	eventually(t, wait, "the move applied at once", func() bool { return v.holds(chatX, move) && v.tasks()[0].Status == "doing" })
}
