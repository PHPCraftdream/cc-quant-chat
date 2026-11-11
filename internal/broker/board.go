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

// changesBoard: the board ops that write; a stopping broker refuses them like other changes.
var changesBoard = map[string]bool{
	"task.create": true, "task.update": true, "task.move": true, "task.delete": true,
	"sprint.create": true, "sprint.update": true, "sprint.start": true, "sprint.close": true,
	"board.revert": true, "board.workflow_set": true,
}

// boardOp serves one request of the board. Any member of the chat sees all of it and may change
// it; every change and its journal entry are written in one transaction.
func (b *Broker) boardOp(ch *chat, m *member, a proto.BoardArgs) (any, *proto.Error) {
	bs, ok := b.store.(store.Board)
	if !ok {
		return nil, proto.Errf("board_unavailable", "the task board needs the SQLite store (the broker runs with CCQC_STORE=memory)")
	}
	if changesBoard[a.Op] {
		if b.stopping.Load() {
			return nil, proto.Errf("restarting", "the broker is restarting; repeat in a moment")
		}
		if !b.allow(ch, m.name, "board", boardLimit) {
			return nil, proto.Errf("rate_limited", "max %d board changes per minute", boardLimit)
		}
		if e := b.checkAssignee(ch, a); e != nil {
			return nil, e
		}
		var change board.Change
		err := bs.Do(ch.id, func(tx store.BoardTx) error {
			var err error
			change, err = applyBoard(board.Ctx{Tx: tx, Actor: m.name, Owner: m.owner, Now: time.Now()}, a)
			return err
		})
		if err != nil {
			return nil, boardError(err)
		}
		b.notifyAssignee(ch, m, change)
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
	text := "board: @" + actor.name + " " + c.Text
	if assigned {
		text = fmt.Sprintf("board: @%s assigned you %s %q (log #%d)", actor.name, board.TaskRef(t.ID), t.Title, c.LogID)
	}
	b.deliver(ch, t.Assignee, proto.Event{Kind: "board", Text: text, Wake: wake})
}
