package board

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// TaskArgs: the fields of a new task.
type TaskArgs struct {
	Title       string
	Description string
	Assignee    string
	Priority    int
	Sprint      int64
}

// TaskPatch: the fields to change; nil leaves a field alone.
type TaskPatch struct {
	Title       *string
	Description *string
	Assignee    *string
	Priority    *int
	Sprint      *int64
}

func validTitle(title string) (string, error) {
	title = oneLine(title)
	if title == "" {
		return "", proto.Errf("bad_args", "a task needs a title")
	}
	if len(title) > MaxTitle {
		return "", proto.Errf("too_large", "title is %d bytes, max %d", len(title), MaxTitle)
	}
	return title, nil
}

func validDescription(d string) error {
	if len(d) > MaxDescription {
		return proto.Errf("too_large", "description is %d bytes, max %d", len(d), MaxDescription)
	}
	return nil
}

func validPriority(p int) error {
	if p < 0 || p > MaxPriority {
		return proto.Errf("bad_args", "priority is 0..%d", MaxPriority)
	}
	return nil
}

// CreateTask adds a task in the workflow's initial status.
func CreateTask(c Ctx, a TaskArgs) (Change, error) {
	title, err := validTitle(a.Title)
	if err != nil {
		return Change{}, err
	}
	if err := validDescription(a.Description); err != nil {
		return Change{}, err
	}
	if err := validPriority(a.Priority); err != nil {
		return Change{}, err
	}
	if err := checkSprintTarget(c.Tx, a.Sprint); err != nil {
		return Change{}, err
	}
	wf, err := workflow(c.Tx)
	if err != nil {
		return Change{}, err
	}
	id, err := c.Tx.NextTaskID()
	if err != nil {
		return Change{}, err
	}
	t := store.Task{ID: id, Title: title, Description: a.Description, Status: wf.Initial, Sprint: a.Sprint,
		Assignee: a.Assignee, Priority: a.Priority, CreatedBy: c.Actor, CreatedAt: c.ts(), UpdatedAt: c.ts()}
	if err := c.Tx.PutTask(t); err != nil {
		return Change{}, err
	}
	logID, err := c.log("task.create", "task", id, nil, taskFields(t, taskFieldNames...), 0)
	if err != nil {
		return Change{}, err
	}
	return Change{
		Text:   fmt.Sprintf("%s created %s%s p%d %q (log #%d)", TaskRef(id), t.Status, assigneeSuffix(t.Assignee), t.Priority, clip(title, TitleInList), logID),
		LogID:  logID,
		Task:   t,
		Fields: []string{"assignee", "status"},
	}, nil
}

func assigneeSuffix(a string) string {
	if a == "" {
		return ""
	}
	return " @" + a
}

// UpdateTask changes title, description, assignee, priority or sprint; only fields that differ
// are journaled. Nothing to change is not an error and writes nothing.
func UpdateTask(c Ctx, id int64, p TaskPatch) (Change, error) {
	t, err := loadTask(c.Tx, id)
	if err != nil {
		return Change{}, err
	}
	before, after := map[string]json.RawMessage{}, map[string]json.RawMessage{}
	var fields []string
	next := t
	change := func(name string, apply func(*store.Task)) {
		old := taskField(next, name)
		apply(&next)
		if nw := taskField(next, name); !sameJSON(old, nw) {
			before[name], after[name] = old, nw
			fields = append(fields, name)
		}
	}
	if p.Title != nil {
		title, err := validTitle(*p.Title)
		if err != nil {
			return Change{}, err
		}
		change("title", func(t *store.Task) { t.Title = title })
	}
	if p.Description != nil {
		if err := validDescription(*p.Description); err != nil {
			return Change{}, err
		}
		change("description", func(t *store.Task) { t.Description = *p.Description })
	}
	if p.Assignee != nil {
		change("assignee", func(t *store.Task) { t.Assignee = *p.Assignee })
	}
	if p.Priority != nil {
		if err := validPriority(*p.Priority); err != nil {
			return Change{}, err
		}
		change("priority", func(t *store.Task) { t.Priority = *p.Priority })
	}
	if p.Sprint != nil {
		if *p.Sprint != t.Sprint {
			if err := checkSprintTarget(c.Tx, *p.Sprint); err != nil {
				return Change{}, err
			}
		}
		change("sprint", func(t *store.Task) { t.Sprint = *p.Sprint })
	}
	if len(fields) == 0 {
		return Change{Text: TaskRef(id) + " unchanged", Task: t}, nil
	}
	next.UpdatedAt = c.ts()
	if err := c.Tx.PutTask(next); err != nil {
		return Change{}, err
	}
	logID, err := c.log("task.update", "task", id, before, after, 0)
	if err != nil {
		return Change{}, err
	}
	return Change{
		Text:   fmt.Sprintf("%s updated: %s (log #%d)", TaskRef(id), strings.Join(fields, ", "), logID),
		LogID:  logID,
		Task:   next,
		Fields: fields,
	}, nil
}

// MoveTask sets the status, following the workflow.
func MoveTask(c Ctx, id int64, status string) (Change, error) {
	t, err := loadTask(c.Tx, id)
	if err != nil {
		return Change{}, err
	}
	wf, err := workflow(c.Tx)
	if err != nil {
		return Change{}, err
	}
	if !contains(wf.Statuses, status) {
		return Change{}, proto.Errf("bad_status", "no status %q in this board's process; statuses: %s", status, strings.Join(wf.Statuses, ", "))
	}
	if t.Status == status {
		return Change{Text: fmt.Sprintf("%s is already %s", TaskRef(id), status), Task: t}, nil
	}
	if !contains(wf.Transitions[t.Status], status) {
		allowed := strings.Join(wf.Transitions[t.Status], ", ")
		if allowed == "" {
			allowed = "none"
		}
		return Change{}, proto.Errf("bad_transition", "%s can't go %s → %s; from %s: %s", TaskRef(id), t.Status, status, t.Status, allowed)
	}
	old := t.Status
	t.Status, t.UpdatedAt = status, c.ts()
	if err := c.Tx.PutTask(t); err != nil {
		return Change{}, err
	}
	logID, err := c.log("task.move", "task", id, map[string]json.RawMessage{"status": raw(old)}, map[string]json.RawMessage{"status": raw(status)}, 0)
	if err != nil {
		return Change{}, err
	}
	return Change{Text: fmt.Sprintf("%s %s → %s (log #%d)", TaskRef(id), old, status, logID), LogID: logID, Task: t, Fields: []string{"status"}}, nil
}

// DeleteTask hides a task (soft delete); the journal can bring it back.
func DeleteTask(c Ctx, id int64) (Change, error) {
	t, err := loadTask(c.Tx, id)
	if err != nil {
		return Change{}, err
	}
	t.Deleted, t.UpdatedAt = true, c.ts()
	if err := c.Tx.PutTask(t); err != nil {
		return Change{}, err
	}
	logID, err := c.log("task.delete", "task", id, map[string]json.RawMessage{"deleted": raw(false)}, map[string]json.RawMessage{"deleted": raw(true)}, 0)
	if err != nil {
		return Change{}, err
	}
	return Change{Text: fmt.Sprintf("%s deleted (log #%d)", TaskRef(id), logID), LogID: logID, Task: t, Fields: []string{"deleted"}}, nil
}
