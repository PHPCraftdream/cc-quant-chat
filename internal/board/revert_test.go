package board

import (
	"fmt"
	"strings"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

func revert(id int64, force bool) func(c Ctx) (Change, error) {
	return func(c Ctx) (Change, error) { return Revert(c, id, force) }
}

func TestRevertAnUpdateAndAMoveAndLogTheReverts(t *testing.T) {
	e := newEnv(t)
	e.must("a", createTask("title")) // #1
	to := "better title"             // update #2
	e.must("a", func(c Ctx) (Change, error) { return UpdateTask(c, 1, TaskPatch{Title: &to}) })
	e.must("a", move(1, "doing")) // #3

	r := e.must("b", revert(3, false))
	if e.task(1).Status != "todo" {
		t.Fatalf("status after reverting the move: %s", e.task(1).Status)
	}
	l := e.log(r.LogID)
	if l.Kind != "revert" || l.Reverts != 3 || l.Actor != "b" || str(l.Before["status"]) != `"doing"` || str(l.After["status"]) != `"todo"` {
		t.Fatalf("the revert is a journal entry: %+v", l)
	}
	e.must("b", revert(2, false))
	if got := e.task(1).Title; got != "title" {
		t.Fatalf("title after reverting the update: %q", got)
	}

	// Revert of a revert repeats the original change.
	e.must("b", revert(r.LogID, false))
	if e.task(1).Status != "doing" {
		t.Fatalf("revert of the revert: status %s, want doing again", e.task(1).Status)
	}
}

func TestRevertOfACreationHidesTheTaskAndRevertingThatBringsItBack(t *testing.T) {
	e := newEnv(t)
	c := e.must("a", createTask("short-lived"))
	r := e.must("a", revert(c.LogID, false))
	if !e.task(1).Deleted {
		t.Fatal("reverting a creation must hide the task")
	}
	if l := e.log(r.LogID); l.Reverts != c.LogID || str(l.After["deleted"]) != "true" {
		t.Fatalf("entry: %+v", l)
	}
	e.must("a", revert(r.LogID, false))
	if e.task(1).Deleted {
		t.Fatal("reverting the revert must bring the task back")
	}
	// A creation that was edited since can't be reverted without force.
	to := "edited"
	e.must("a", func(c Ctx) (Change, error) { return UpdateTask(c, 1, TaskPatch{Title: &to}) })
	if _, err := e.do("a", revert(c.LogID, false)); code(err) != "conflict" {
		t.Fatalf("revert of a creation after an edit: %v", err)
	}
	e.must("a", revert(c.LogID, true))
	if !e.task(1).Deleted {
		t.Fatal("force must hide the task")
	}
}

func TestRevertConflictNamesTheLaterEntriesAndForceOverrides(t *testing.T) {
	e := newEnv(t)
	e.must("a", createTask("t"))   // #1
	e.must("a", move(1, "doing"))  // #2
	e.must("b", move(1, "review")) // #3 touches the same field later
	_, err := e.do("a", revert(2, false))
	if code(err) != "conflict" || !strings.Contains(err.Error(), "#3") || !strings.Contains(err.Error(), "status") {
		t.Fatalf("conflict: %v; want the field and the later entry #3", err)
	}
	if e.task(1).Status != "review" {
		t.Fatal("a refused revert changed the task")
	}
	// Reverting the later one first resolves it.
	e.must("a", revert(3, false))
	e.must("a", revert(2, false))
	if e.task(1).Status != "todo" {
		t.Fatalf("status = %s", e.task(1).Status)
	}
	// With force the earlier value is put back whatever the current one is.
	e.must("a", move(1, "doing"))  // #6
	e.must("b", move(1, "review")) // #7
	e.must("a", revert(6, true))
	if e.task(1).Status != "todo" {
		t.Fatalf("forced revert: status %s", e.task(1).Status)
	}
}

func TestRevertNeedsTheStatusToExistInTheCurrentProcess(t *testing.T) {
	e := newEnv(t)
	e.must("a", createTask("t"))
	e.must("a", move(1, "doing"))
	mv := e.must("a", move(1, "review")) // its earlier status is "doing"
	e.must("a", move(1, "done"))
	// Another process that has no "doing"; the task is "done" in both, so it can be installed.
	wf := store.Workflow{Statuses: []string{"todo", "done"}, Initial: "todo", Terminal: []string{"done"},
		Transitions: map[string][]string{"todo": {"done"}, "done": {"todo"}}}
	e.must("a", func(c Ctx) (Change, error) { return SetWorkflow(c, wf) })
	_, err := e.do("a", revert(mv.LogID, true))
	if code(err) != "bad_status" {
		t.Fatalf("reverting into a status the process lacks: %v", err)
	}
}

func TestRevertAWorkflowChange(t *testing.T) {
	e := newEnv(t)
	wf := store.Workflow{Statuses: []string{"todo", "ok"}, Initial: "todo", Terminal: []string{"ok"}, Transitions: map[string][]string{"todo": {"ok"}}}
	set := e.must("a", func(c Ctx) (Change, error) { return SetWorkflow(c, wf) })
	e.must("a", revert(set.LogID, false))
	var got store.Workflow
	e.view(chatA, func(tx store.BoardTx) error {
		var err error
		got, _, err = GetWorkflow(tx)
		return err
	})
	if len(got.Statuses) != 6 || got.Initial != "todo" {
		t.Fatalf("the default process is back: %+v", got)
	}
}

func TestRevertAClosedSprintBringsTheCarriedTasksBack(t *testing.T) {
	e := newEnv(t)
	e.must("a", func(c Ctx) (Change, error) { return CreateSprint(c, "one", "") })
	e.must("a", func(c Ctx) (Change, error) { return StartSprint(c, 1) })
	e.must("a", func(c Ctx) (Change, error) { return CreateTask(c, TaskArgs{Title: "x", Sprint: 1}) })
	e.must("a", func(c Ctx) (Change, error) { return CreateTask(c, TaskArgs{Title: "y", Sprint: 1}) })
	closed := e.must("a", func(c Ctx) (Change, error) { return CloseSprint(c, 1, 0) })
	if e.task(1).Sprint != 0 {
		t.Fatal("not carried to the backlog")
	}

	r := e.must("a", revert(closed.LogID, false))
	if e.task(1).Sprint != 1 || e.task(2).Sprint != 1 {
		t.Fatalf("carried tasks after the revert: %d %d, want both in S-1", e.task(1).Sprint, e.task(2).Sprint)
	}
	var sp store.Sprint
	e.view(chatA, func(tx store.BoardTx) error { var err error; sp, _, err = tx.GetSprint(1); return err })
	if sp.State != store.SprintActive || sp.ClosedAt != "" {
		t.Fatalf("sprint after the revert: %+v", sp)
	}
	if l := e.log(r.LogID); l.Kind != "revert" || l.Reverts != closed.LogID {
		t.Fatalf("entry: %+v", l)
	}

	// Reverting the revert closes it again and carries the tasks again.
	e.must("a", revert(r.LogID, false))
	if e.task(1).Sprint != 0 || e.task(2).Sprint != 0 {
		t.Fatalf("after the revert of the revert: %d %d, want the backlog", e.task(1).Sprint, e.task(2).Sprint)
	}

	// A carried task that was moved on since is a conflict.
	e.must("a", revert(r.LogID+1, false)) // back to active, tasks in S-1
	e.must("a", func(c Ctx) (Change, error) { return CloseSprint(c, 1, 0) })
	e.must("a", func(c Ctx) (Change, error) { return CreateSprint(c, "two", "") })
	to := int64(2)
	e.must("a", func(c Ctx) (Change, error) { return UpdateTask(c, 1, TaskPatch{Sprint: &to}) })
	var lastClose int64
	e.view(chatA, func(tx store.BoardTx) error {
		l, err := tx.LogFor("sprint", 1, 0, 1)
		if err == nil {
			for _, x := range l {
				lastClose = x.ID
			}
		}
		return err
	})
	if _, err := e.do("a", revert(lastClose, false)); code(err) != "conflict" || !strings.Contains(err.Error(), "T-1") {
		t.Fatalf("reverting a close whose carried task was moved on: %v", err)
	}
}

func TestRevertOfASprintCreationNeedsAnEmptySprint(t *testing.T) {
	e := newEnv(t)
	c := e.must("a", func(c Ctx) (Change, error) { return CreateSprint(c, "one", "") })
	e.must("a", func(c Ctx) (Change, error) { return CreateTask(c, TaskArgs{Title: "x", Sprint: 1}) })
	if _, err := e.do("a", revert(c.LogID, false)); code(err) != "sprint_not_empty" {
		t.Fatalf("sprint with a task: %v", err)
	}
	e.must("a", func(c Ctx) (Change, error) {
		zero := int64(0)
		return UpdateTask(c, 1, TaskPatch{Sprint: &zero})
	})
	e.must("a", revert(c.LogID, false))
	var list []store.Sprint
	e.view(chatA, func(tx store.BoardTx) error { var err error; list, err = tx.ListSprints(); return err })
	if len(list) != 0 {
		t.Fatalf("the reverted sprint is still listed: %+v", list)
	}
}

func TestNoSuchEntryAndForeignEntriesCantBeReverted(t *testing.T) {
	e := newEnv(t)
	if _, err := e.do("a", revert(99, false)); code(err) != "no_such_log" {
		t.Fatalf("missing entry: %v", err)
	}
}

func TestListIsShortByDefault(t *testing.T) {
	e := newEnv(t)
	e.must("a", func(c Ctx) (Change, error) { return CreateSprint(c, "now", "") })
	e.must("a", func(c Ctx) (Change, error) { return StartSprint(c, 1) })
	e.must("a", func(c Ctx) (Change, error) {
		return CreateTask(c, TaskArgs{Title: "in the sprint", Description: "SECRET-LONG-DESCRIPTION", Sprint: 1, Assignee: "alice", Priority: 2})
	})
	e.must("a", createTask("in the backlog"))
	e.must("a", func(c Ctx) (Change, error) { return CreateTask(c, TaskArgs{Title: "finished", Sprint: 1}) })
	e.must("a", move(3, "doing"))
	e.must("a", move(3, "review"))
	e.must("a", move(3, "done"))

	list := func(a ListArgs) string {
		var out string
		e.view(chatA, func(tx store.BoardTx) error {
			var err error
			out, err = List(tx, "alice", a)
			return err
		})
		return out
	}
	def := list(ListArgs{})
	if !strings.Contains(def, `Sprint S-1 "now" (active)`) || !strings.Contains(def, "T-1 todo @alice p2 in the sprint") {
		t.Fatalf("default list:\n%s", def)
	}
	for _, not := range []string{"SECRET-LONG-DESCRIPTION", "in the backlog", "finished"} {
		if strings.Contains(def, not) {
			t.Fatalf("the default list shows %q:\n%s", not, def)
		}
	}
	if !strings.Contains(def, "done 1") || !strings.Contains(def, "todo 1") {
		t.Fatalf("counters by status are missing:\n%s", def)
	}
	if all := list(ListArgs{Sprint: "all", Done: true}); !strings.Contains(all, "in the backlog") || !strings.Contains(all, "finished") {
		t.Fatalf("sprint=all done=true:\n%s", all)
	}
	if bl := list(ListArgs{Sprint: "backlog"}); !strings.Contains(bl, "Backlog") || !strings.Contains(bl, "in the backlog") || strings.Contains(bl, "in the sprint") {
		t.Fatalf("backlog:\n%s", bl)
	}
	if mine := list(ListArgs{Mine: true}); !strings.Contains(mine, "in the sprint") {
		t.Fatalf("mine:\n%s", mine)
	}
	if one := list(ListArgs{Sprint: "all", Status: "done"}); !strings.Contains(one, "finished") || strings.Contains(one, "in the sprint") {
		t.Fatalf("status filter:\n%s", one)
	}
}

func TestListIsCappedAndGetShowsTheDescriptionAndRecentJournal(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < ListLines+7; i++ {
		e.must("a", createTask(fmt.Sprintf("task %d", i)))
	}
	var out string
	e.view(chatA, func(tx store.BoardTx) error { var err error; out, err = List(tx, "a", ListArgs{}); return err })
	if n := strings.Count(out, "\nT-") + 1; n > ListLines+1 {
		t.Fatalf("%d task lines, want at most %d", n, ListLines)
	}
	if !strings.Contains(out, "…and 7 more") {
		t.Fatalf("no count of the rest:\n%s", out)
	}

	e.must("a", func(c Ctx) (Change, error) {
		d := "the full description"
		return UpdateTask(c, 1, TaskPatch{Description: &d})
	})
	for _, s := range []string{"doing", "review", "doing", "review", "doing", "review"} {
		e.must("a", move(1, s))
	}
	e.view(chatA, func(tx store.BoardTx) error { var err error; out, err = Get(tx, 1); return err })
	if !strings.Contains(out, "the full description") || strings.Count(out, "task.move") != 5 {
		t.Fatalf("task get: the description and the five newest journal entries (all moves) expected:\n%s", out)
	}
}
