package board

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// Revert undoes a journal entry: for every field the entry set, the current value must still be
// the one it set (else the later entries that touched the field are named, and force overrides),
// then the entry's earlier values are put back. The revert is a journal entry of its own, so it
// can be reverted too, which repeats the original change.
func Revert(c Ctx, logID int64, force bool) (Change, error) {
	e, ok, err := c.Tx.GetLog(logID)
	if err != nil {
		return Change{}, err
	}
	if !ok {
		return Change{}, proto.Errf("no_such_log", "there is no journal entry #%d", logID)
	}
	switch e.Entity {
	case "task":
		return revertTask(c, e, force)
	case "sprint":
		return revertSprint(c, e, force)
	case "workflow":
		return revertWorkflow(c, e, force)
	}
	return Change{}, proto.Errf("bad_args", "entry #%d (%s) can't be reverted", logID, e.Kind)
}

// pseudo fields of sprint.close: where the carried tasks went; not columns of the sprint.
func isPseudo(f string) bool { return f == "carried" || f == "carry_to" }

func sortedFields(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for f := range m {
		if !isPseudo(f) {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

// conflictError names the fields whose value moved on since the entry and the later entries
// that moved them.
func conflictError(c Ctx, e store.LogEntry, fields []string, show func(string) string) error {
	later, err := c.Tx.LogsAfter(e.Entity, e.EntityID, e.ID)
	if err != nil {
		return err
	}
	var ids []string
	var changed []string
	for _, l := range later {
		for _, f := range fields {
			if _, touched := l.After[f]; touched {
				ids = append(ids, fmt.Sprintf("#%d", l.ID))
				break
			}
		}
	}
	for _, f := range fields {
		if v, ok := e.After[f]; ok {
			changed = append(changed, fmt.Sprintf("%s is now %s (entry #%d set %s)", f, show(f), e.ID, short(v)))
		} else {
			changed = append(changed, f) // a carried task that was moved on
		}
	}
	by := ""
	if len(ids) > 0 {
		by = "; later entries: " + strings.Join(ids, ", ")
	}
	return proto.Errf("conflict", "can't revert #%d: %s%s. Revert the later entries first, or use force", e.ID, strings.Join(changed, "; "), by)
}

func short(v json.RawMessage) string {
	s := string(v)
	if len(s) > 40 {
		s = s[:39] + "…"
	}
	return s
}

// ---- tasks ----

func revertTask(c Ctx, e store.LogEntry, force bool) (Change, error) {
	if _, isComment := e.After["comment"]; isComment {
		return revertComment(c, e, force)
	}
	t, ok, err := c.Tx.GetTask(e.EntityID)
	if err != nil {
		return Change{}, err
	}
	if !ok {
		return Change{}, errNoTask(e.EntityID)
	}
	fields := sortedFields(e.After)
	if !force {
		var moved []string
		for _, f := range fields {
			if !sameJSON(taskField(t, f), e.After[f]) {
				moved = append(moved, f)
			}
		}
		if len(moved) > 0 {
			return Change{}, conflictError(c, e, moved, func(f string) string { return short(taskField(t, f)) })
		}
	}
	target := e.Before
	if e.Kind == "task.create" { // undoing a creation hides the task
		target, fields = map[string]json.RawMessage{"deleted": raw(true)}, []string{"deleted"}
	}
	before, after := map[string]json.RawMessage{}, map[string]json.RawMessage{}
	next := t
	for _, f := range fields {
		v, ok := target[f]
		if !ok {
			return Change{}, proto.Errf("bad_args", "entry #%d has no earlier value for %s", e.ID, f)
		}
		before[f], after[f] = taskField(next, f), v
		if err := setTaskField(&next, f, v); err != nil {
			return Change{}, err
		}
	}
	if _, moved := after["status"]; moved {
		wf, err := workflow(c.Tx)
		if err != nil {
			return Change{}, err
		}
		if !contains(wf.Statuses, next.Status) {
			return Change{}, proto.Errf("bad_status", "status %q no longer exists in the process; the revert would leave %s in it", next.Status, TaskRef(t.ID))
		}
	}
	if _, moved := after["sprint"]; moved && next.Sprint != t.Sprint {
		if s, ok, err := c.Tx.GetSprint(next.Sprint); err != nil {
			return Change{}, err
		} else if next.Sprint != 0 && (!ok || s.Deleted) {
			return Change{}, errNoSprint(next.Sprint)
		}
	}
	next.UpdatedAt = c.ts()
	if err := c.Tx.PutTask(next); err != nil {
		return Change{}, err
	}
	logID, err := c.log("revert", "task", t.ID, before, after, e.ID)
	if err != nil {
		return Change{}, err
	}
	return Change{Text: fmt.Sprintf("reverted #%d: %s %s (log #%d)", e.ID, TaskRef(t.ID), describe(before, after), logID), LogID: logID, Task: next, Fields: fields}, nil
}

// describe: "status doing → todo, assignee bob → alice".
func describe(before, after map[string]json.RawMessage) string {
	var parts []string
	for _, f := range sortedFields(after) {
		parts = append(parts, fmt.Sprintf("%s %s → %s", f, short(before[f]), short(after[f])))
	}
	return strings.Join(parts, ", ")
}

// ---- sprints ----

func revertSprint(c Ctx, e store.LogEntry, force bool) (Change, error) {
	s, ok, err := c.Tx.GetSprint(e.EntityID)
	if err != nil {
		return Change{}, err
	}
	if !ok {
		return Change{}, errNoSprint(e.EntityID)
	}
	fields := sortedFields(e.After)

	var carried []int64
	var carryAfter, carryBefore int64
	if v, has := e.After["carried"]; has {
		if err := json.Unmarshal(v, &carried); err != nil {
			return Change{}, err
		}
		_ = json.Unmarshal(e.After["carry_to"], &carryAfter)
		_ = json.Unmarshal(e.Before["carry_to"], &carryBefore)
	}

	if !force {
		var moved []string
		for _, f := range fields {
			if !sameJSON(sprintField(s, f), e.After[f]) {
				moved = append(moved, f)
			}
		}
		for _, id := range carried { // carried tasks must still be where the entry put them
			t, ok, err := c.Tx.GetTask(id)
			if err != nil {
				return Change{}, err
			}
			if ok && !t.Deleted && t.Sprint != carryAfter {
				moved = append(moved, fmt.Sprintf("%s (now in %s)", TaskRef(id), sprintLabel(t.Sprint)))
			}
		}
		if len(moved) > 0 {
			return Change{}, conflictError(c, e, moved, func(f string) string { return short(sprintField(s, f)) })
		}
	}
	target := e.Before
	if e.Kind == "sprint.create" { // undoing a creation hides the sprint, if it holds nothing
		live, err := c.Tx.ListTasks(store.TaskFilter{Sprint: s.ID})
		if err != nil {
			return Change{}, err
		}
		if len(live) > 0 {
			return Change{}, proto.Errf("sprint_not_empty", "%s holds %d task(s); move them out first", SprintRef(s.ID), len(live))
		}
		target, fields = map[string]json.RawMessage{"deleted": raw(true)}, []string{"deleted"}
	}
	before, after := map[string]json.RawMessage{}, map[string]json.RawMessage{}
	next := s
	for _, f := range fields {
		v, ok := target[f]
		if !ok {
			return Change{}, proto.Errf("bad_args", "entry #%d has no earlier value for %s", e.ID, f)
		}
		before[f], after[f] = sprintField(next, f), v
		if err := setSprintField(&next, f, v); err != nil {
			return Change{}, err
		}
	}
	if next.State == store.SprintActive && s.State != store.SprintActive {
		if act, ok, err := c.Tx.ActiveSprint(); err != nil {
			return Change{}, err
		} else if ok && act.ID != s.ID {
			return Change{}, proto.Errf("sprint_active", "%s is active; close it first", SprintRef(act.ID))
		}
	}
	if len(carried) > 0 {
		if carryBefore != s.ID {
			if err := checkSprintTarget(c.Tx, carryBefore); err != nil {
				return Change{}, err
			}
		}
		for _, id := range carried {
			t, ok, err := c.Tx.GetTask(id)
			if err != nil {
				return Change{}, err
			}
			if !ok || t.Deleted {
				continue
			}
			t.Sprint, t.UpdatedAt = carryBefore, c.ts()
			if err := c.Tx.PutTask(t); err != nil {
				return Change{}, err
			}
		}
		before["carried"], after["carried"] = raw(carried), raw(carried)
		before["carry_to"], after["carry_to"] = raw(carryAfter), raw(carryBefore)
	}
	if err := c.Tx.PutSprint(next); err != nil {
		return Change{}, err
	}
	logID, err := c.log("revert", "sprint", s.ID, before, after, e.ID)
	if err != nil {
		return Change{}, err
	}
	return Change{Text: fmt.Sprintf("reverted #%d: %s %s (log #%d)", e.ID, SprintRef(s.ID), describe(before, after), logID), LogID: logID}, nil
}

func sprintLabel(id int64) string {
	if id == 0 {
		return "the backlog"
	}
	return SprintRef(id)
}

// ---- workflow ----

func revertWorkflow(c Ctx, e store.LogEntry, force bool) (Change, error) {
	cur, err := workflow(c.Tx)
	if err != nil {
		return Change{}, err
	}
	if !force && !sameJSON(raw(cur), e.After["workflow"]) {
		return Change{}, conflictError(c, e, []string{"workflow"}, func(string) string { return "different" })
	}
	var old store.Workflow
	if err := json.Unmarshal(e.Before["workflow"], &old); err != nil {
		return Change{}, err
	}
	logID, err := putWorkflow(c, old, "revert", e.ID)
	if err != nil {
		return Change{}, err
	}
	return Change{Text: fmt.Sprintf("reverted #%d: process restored (log #%d)", e.ID, logID), LogID: logID}, nil
}
