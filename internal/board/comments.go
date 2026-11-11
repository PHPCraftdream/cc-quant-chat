package board

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// MaxComment is the longest comment, in bytes.
const MaxComment = 4 << 10

// Comments shown by `task get`, and the page size of `task comments`.
const (
	CommentsInGet   = 3
	CommentsDefault = 10
	CommentsMax     = 50
)

// CommentTask adds a comment to a live task. Its journal entry carries the text, so the comment
// can be rebuilt from the journal; hiding it later is a revert.
func CommentTask(c Ctx, taskID int64, text string) (Change, error) {
	t, err := loadTask(c.Tx, taskID)
	if err != nil {
		return Change{}, err
	}
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		return Change{}, proto.Errf("bad_args", "a comment needs text")
	}
	if len(text) > MaxComment {
		return Change{}, proto.Errf("too_large", "comment is %d bytes, max %d", len(text), MaxComment)
	}
	id, err := c.Tx.NextCommentID()
	if err != nil {
		return Change{}, err
	}
	after := map[string]json.RawMessage{"comment": raw(id), "text": raw(text), "hidden": raw(false)}
	logID, lamport, err := c.logL("task.comment", "task", taskID, nil, after, 0)
	if err != nil {
		return Change{}, err
	}
	if err := c.Tx.PutComment(store.Comment{ID: id, TaskID: taskID, TS: c.ts(), Author: c.Actor, AuthorOwner: c.Owner,
		Text: text, Origin: c.Origin, Lamport: lamport, AuthorPub: c.Sig.Pub, Sig: c.Sig.Sig}); err != nil {
		return Change{}, err
	}
	ch := Change{Text: fmt.Sprintf("%s commented (comment #%d, log #%d)", TaskRef(taskID), id, logID), LogID: logID, Task: t, Fields: []string{"comment"}}
	// The assignee is woken by a comment, the task's author hears of it quietly; neither is told
	// of their own comment.
	msg := fmt.Sprintf("@%s commented on %s %q: %s", c.Actor, TaskRef(taskID), clip(t.Title, 40), clip(oneLine(text), 120))
	if t.Assignee != "" && t.Assignee != c.Actor {
		ch.Notes = append(ch.Notes, Note{To: t.Assignee, Text: msg, Wake: true})
	}
	if t.CreatedBy != "" && t.CreatedBy != c.Actor && t.CreatedBy != t.Assignee {
		ch.Notes = append(ch.Notes, Note{To: t.CreatedBy, Text: msg})
	}
	return ch, nil
}

// revertComment hides a comment (or, for the revert of such a hiding, shows it again).
func revertComment(c Ctx, e store.LogEntry, force bool) (Change, error) {
	var cid int64
	if err := json.Unmarshal(e.After["comment"], &cid); err != nil {
		return Change{}, err
	}
	cm, ok, err := c.Tx.GetComment(cid)
	if err != nil {
		return Change{}, err
	}
	if !ok {
		return Change{}, proto.Errf("no_such_comment", "there is no comment #%d", cid)
	}
	var wasHidden bool
	_ = json.Unmarshal(e.After["hidden"], &wasHidden)
	if !force && cm.Deleted != wasHidden {
		return Change{}, conflictError(c, e, []string{"hidden"}, func(string) string { return fmt.Sprint(cm.Deleted) })
	}
	target := true // undoing a comment hides it
	if v, has := e.Before["hidden"]; has {
		_ = json.Unmarshal(v, &target)
	}
	before := map[string]json.RawMessage{"comment": raw(cid), "hidden": raw(cm.Deleted)}
	after := map[string]json.RawMessage{"comment": raw(cid), "hidden": raw(target)}
	cm.Deleted = target
	if err := c.Tx.PutComment(cm); err != nil {
		return Change{}, err
	}
	logID, err := c.log("revert", "task", e.EntityID, before, after, e.ID)
	if err != nil {
		return Change{}, err
	}
	verb := "hidden"
	if !target {
		verb = "shown again"
	}
	return Change{Text: fmt.Sprintf("reverted #%d: comment #%d %s (log #%d)", e.ID, cid, verb, logID), LogID: logID}, nil
}

func commentLine(cm store.Comment) string {
	who := "@" + cm.Author
	if cm.AuthorOwner {
		who = "OWNER " + who
	}
	return fmt.Sprintf("#%d %s %s: %s", cm.ID, shortTS(cm.TS), who, cm.Text)
}

// Comments renders one page of a task's comments, newest first; before pages back.
func Comments(tx store.BoardTx, taskID int64, limit int, before int64) (string, error) {
	if _, ok, err := tx.GetTask(taskID); err != nil {
		return "", err
	} else if !ok {
		return "", errNoTask(taskID)
	}
	if limit <= 0 {
		limit = CommentsDefault
	}
	if limit > CommentsMax {
		limit = CommentsMax
	}
	list, err := tx.ListComments(taskID, before, limit)
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "no comments here\n", nil
	}
	var b strings.Builder
	for _, cm := range list {
		b.WriteString(commentLine(cm) + "\n")
	}
	if len(list) == limit {
		fmt.Fprintf(&b, "(older: before=%d)\n", list[len(list)-1].ID)
	}
	return b.String(), nil
}
