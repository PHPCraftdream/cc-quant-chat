package broker

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/board"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

const boardLimit = 60 // board changes per minute per nick

// boardOp serves one request of the board. Any member of the chat sees all of it and may change
// it; every change and its journal entry are written in one transaction.
func (b *Broker) boardOp(ch *chat, m *member, a proto.BoardArgs, raw json.RawMessage) (any, *proto.Error) {
	bs, ok := b.store.(store.Board)
	if !ok {
		return nil, proto.Errf("board_unavailable", "the task board needs the SQLite store (the broker runs with CCQC_STORE=memory)")
	}
	if proto.BoardWrite(a.Op) {
		if b.stopping.Load() {
			return nil, proto.Errf("restarting", "the broker is restarting; repeat in a moment")
		}
		sg, e := b.verifyWrite(ch, m, a.Op, raw)
		if e != nil {
			return nil, e
		}
		if !b.allow(ch, m.name, "board", boardLimit) {
			return nil, proto.Errf("rate_limited", "max %d board changes per minute", boardLimit)
		}
		if e := b.checkAssignee(ch, a); e != nil {
			return nil, e
		}
		var change board.Change
		who := b.actorOf(ch, m)
		manager := b.managerRole(ch.id)
		err := bs.Do(ch.id, func(tx store.BoardTx) error {
			if err := b.authorize(tx, who, manager, a); err != nil {
				return err
			}
			now := time.Now()
			var err error
			if change, err = applyBoard(board.Ctx{Tx: tx, Actor: m.name, Owner: m.owner, Grant: who.grant, Origin: b.origin, Sig: sg, Now: now}, a); err != nil {
				return err
			}
			return tx.RecordEvent(store.EventIn{Kind: a.Op, Payload: sg.Canon, AuthorPub: sg.Pub, Sig: sg.Sig,
				Actor: m.name, Owner: m.owner, Grant: who.grant, TS: now.UTC().Format(time.RFC3339)})
		})
		if err != nil {
			return nil, boardError(err)
		}
		if len(change.Notes) > 0 {
			for _, n := range change.Notes {
				b.deliver(ch, n.To, proto.Event{Kind: "board", Text: n.Text, Wake: n.Wake})
			}
		} else {
			b.notifyAssignee(ch, m, change)
		}
		return proto.BoardReply{Text: change.Text}, nil
	}
	var text string
	err := bs.View(ch.id, func(tx store.BoardTx) error {
		var err error
		text, err = readBoard(tx, m.name, a)
		return err
	})
	if err != nil {
		return nil, boardError(err)
	}
	return proto.BoardReply{Text: text}, nil
}

func boardError(err error) *proto.Error {
	var pe *proto.Error
	if errors.As(err, &pe) {
		return pe
	}
	return proto.Errf("store_failed", "the board could not be read or saved: %v", err)
}

// checkAssignee: an assignee must be a nick of this chat.
func (b *Broker) checkAssignee(ch *chat, a proto.BoardArgs) *proto.Error {
	if a.Assignee == nil || *a.Assignee == "" || (a.Op != "task.create" && a.Op != "task.update") {
		return nil
	}
	if ch.nicks[strings.TrimPrefix(*a.Assignee, "@")] == nil {
		return proto.Errf("no_such_member", "no nick @%s in this chat", *a.Assignee)
	}
	return nil
}

func applyBoard(c board.Ctx, a proto.BoardArgs) (board.Change, error) {
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	switch a.Op {
	case "task.create":
		args := board.TaskArgs{Title: str(a.Title), Description: str(a.Description), Assignee: strings.TrimPrefix(str(a.Assignee), "@")}
		if a.Priority != nil {
			args.Priority = *a.Priority
		}
		if a.Sprint != nil {
			args.Sprint = *a.Sprint
		}
		return board.CreateTask(c, args)
	case "task.update":
		p := board.TaskPatch{Title: a.Title, Description: a.Description, Priority: a.Priority, Sprint: a.Sprint}
		if a.Assignee != nil {
			as := strings.TrimPrefix(*a.Assignee, "@")
			p.Assignee = &as
		}
		return board.UpdateTask(c, a.ID, p)
	case "task.move":
		return board.MoveTask(c, a.ID, a.Status)
	case "task.delete":
		return board.DeleteTask(c, a.ID)
	case "task.comment":
		return board.CommentTask(c, a.ID, a.Text)
	case "sprint.create":
		return board.CreateSprint(c, str(a.Name), str(a.Goal))
	case "sprint.update":
		return board.UpdateSprint(c, a.ID, a.Name, a.Goal)
	case "sprint.start":
		return board.StartSprint(c, a.ID)
	case "sprint.close":
		return board.CloseSprint(c, a.ID, a.Carry)
	case "board.revert":
		return board.Revert(c, a.LogID, a.Force)
	case "board.workflow_set":
		var w store.Workflow
		if err := json.Unmarshal(a.Workflow, &w); err != nil {
			return board.Change{}, proto.Errf("bad_args", "workflow is not a process: %v", err)
		}
		return board.SetWorkflow(c, w)
	}
	return board.Change{}, proto.Errf("unknown_op", "board op %q", a.Op)
}

func readBoard(tx store.BoardTx, actor string, a proto.BoardArgs) (string, error) {
	switch a.Op {
	case "task.get":
		return board.Get(tx, a.ID)
	case "task.comments":
		return board.Comments(tx, a.ID, a.Limit, a.Before)
	case "task.list":
		return board.List(tx, actor, board.ListArgs{Sprint: a.SprintFilter, Status: a.Status,
			Assignee: strings.TrimPrefix(a.AssigneeFilter, "@"), Mine: a.Mine, Done: a.Done})
	case "sprint.list":
		return board.Sprints(tx)
	case "board.log":
		switch a.Entity {
		case "", "board":
			return board.Log(tx, "", 0, a.Limit, a.Before)
		case "task", "sprint":
			return board.Log(tx, a.Entity, a.ID, a.Limit, a.Before)
		}
		return "", proto.Errf("bad_args", "entity is task or sprint")
	case "board.workflow_get":
		w, isDefault, err := board.GetWorkflow(tx)
		if err != nil {
			return "", err
		}
		return board.FormatWorkflow(w, isDefault), nil
	}
	return "", proto.Errf("unknown_op", "board op %q", a.Op)
}

// notifyAssignee tells the assignee of a changed task, unless they made the change themselves.
// Only that one person hears of it, and only if present. A new assignment and a change of status
// wake the session; the rest arrives quietly, with the next wake-up.
func (b *Broker) notifyAssignee(ch *chat, actor *member, c board.Change) {
	t := c.Task
	if t.ID == 0 || t.Assignee == "" || t.Assignee == actor.name {
		return
	}
	wake, assigned := false, false
	for _, f := range c.Fields {
		switch f {
		case "assignee":
			wake, assigned = true, true
		case "status":
			wake = true
		}
	}
	text := "@" + actor.name + " " + c.Text
	if assigned {
		text = fmt.Sprintf("@%s assigned you %s %q (log #%d)", actor.name, board.TaskRef(t.ID), t.Title, c.LogID)
	}
	b.deliver(ch, t.Assignee, proto.Event{Kind: "board", Text: text, Wake: wake})
}

// actor is who asks to change the board: the nick, whether the connection proved it holds the
// owner key, and the role the owner pinned to it (written to the journal as the actor's grant).
type actor struct {
	nick  string
	owner bool
	grant string
}

func (b *Broker) actorOf(ch *chat, m *member) actor {
	a := actor{nick: m.name, owner: m.owner}
	if nk := ch.nicks[m.name]; nk != nil {
		a.grant = nk.pinned
	}
	return a
}

// authorize is the one place that decides whether an actor may make a board change.
func (b *Broker) authorize(tx store.BoardTx, who actor, manager string, a proto.BoardArgs) error {
	if manager == "" {
		return nil // no manager role set: every member may do everything
	}
	var t *store.Task
	if strings.HasPrefix(a.Op, "task.") && a.Op != "task.create" {
		if task, ok, err := tx.GetTask(a.ID); err != nil {
			return err
		} else if ok {
			t = &task
		}
	}
	var terminal []string
	if a.Op == "task.move" {
		wf, _, err := board.GetWorkflow(tx)
		if err != nil {
			return err
		}
		terminal = wf.Terminal
	}
	if e := canBoard(who, manager, terminal, a, t); e != nil {
		return e
	}
	return nil
}

// canBoard, with board.manager_role set: the owner and the member holding that pinned role may
// change a task's status, start and close sprints, change the process and revert. The assignee
// may move its own task between unfinished states; finishing it is the manager's. Creating,
// editing, commenting and reading stay open to everyone.
func canBoard(who actor, manager string, terminal []string, a proto.BoardArgs, t *store.Task) *proto.Error {
	isManager := who.owner || who.grant == manager
	switch a.Op {
	case "task.move":
		if isManager {
			return nil
		}
		if t != nil && t.Assignee == who.nick && !containsStr(terminal, a.Status) && !containsStr(terminal, t.Status) {
			return nil
		}
	case "sprint.start", "sprint.close", "board.workflow_set", "board.revert":
		if isManager {
			return nil
		}
	default:
		return nil
	}
	return proto.Errf("forbidden", "%s is for the owner or a member with the pinned role %q (board.manager_role)%s",
		a.Op, manager, assigneeHint(a.Op))
}

func assigneeHint(op string) string {
	if op == "task.move" {
		return "; the assignee may move its own task between unfinished states, finishing it is for them"
	}
	return ""
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
