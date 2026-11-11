package board

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store/sqlite"
)

const chatA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const chatB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

type env struct {
	t    *testing.T
	db   *sqlite.DB
	path string
	now  time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ccqc.db")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, id := range []string{chatA, chatB} {
		if _, err := db.Load(id, "team"); err != nil {
			t.Fatal(err)
		}
	}
	return &env{t: t, db: db, path: path, now: time.Date(2026, 11, 11, 14, 0, 0, 0, time.UTC)}
}

// do runs fn as actor in one transaction on chat A.
func (e *env) do(actor string, fn func(c Ctx) (Change, error)) (Change, error) {
	e.t.Helper()
	e.now = e.now.Add(time.Minute)
	var ch Change
	err := e.db.Do(chatA, func(tx store.BoardTx) error {
		var err error
		ch, err = fn(Ctx{Tx: tx, Actor: actor, Origin: "00112233445566778899aabbccddeeff", Now: e.now})
		return err
	})
	return ch, err
}

func (e *env) must(actor string, fn func(c Ctx) (Change, error)) Change {
	e.t.Helper()
	ch, err := e.do(actor, fn)
	if err != nil {
		e.t.Fatalf("%v", err)
	}
	return ch
}

func (e *env) view(chat string, fn func(tx store.BoardTx) error) {
	e.t.Helper()
	if err := e.db.View(chat, fn); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) task(id int64) store.Task {
	e.t.Helper()
	var t store.Task
	e.view(chatA, func(tx store.BoardTx) error {
		var err error
		t, _, err = tx.GetTask(id)
		return err
	})
	return t
}

func (e *env) log(id int64) store.LogEntry {
	e.t.Helper()
	var l store.LogEntry
	e.view(chatA, func(tx store.BoardTx) error {
		var ok bool
		var err error
		l, ok, err = tx.GetLog(id)
		if err == nil && !ok {
			err = errors.New("no such log entry")
		}
		return err
	})
	return l
}

func code(err error) string {
	var pe *proto.Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	if err == nil {
		return ""
	}
	return "other: " + err.Error()
}

func str(v json.RawMessage) string { return string(v) }

func createTask(title string) func(c Ctx) (Change, error) {
	return func(c Ctx) (Change, error) { return CreateTask(c, TaskArgs{Title: title, Priority: 1}) }
}

func move(id int64, status string) func(c Ctx) (Change, error) {
	return func(c Ctx) (Change, error) { return MoveTask(c, id, status) }
}

func TestEveryChangeIsJournaledWithBeforeAndAfter(t *testing.T) {
	e := newEnv(t)
	sp := e.must("alice", func(c Ctx) (Change, error) { return CreateSprint(c, "Sprint one", "ship it") })
	if sp.LogID != 1 {
		t.Fatalf("first entry id = %d", sp.LogID)
	}
	e.must("alice", func(c Ctx) (Change, error) { return StartSprint(c, 1) })
	created := e.must("alice", func(c Ctx) (Change, error) {
		return CreateTask(c, TaskArgs{Title: "Fix  CI\nnow", Description: "details", Assignee: "bob", Priority: 2, Sprint: 1})
	})
	if created.Task.ID != 1 || created.Task.Status != "todo" || created.Task.Title != "Fix CI now" {
		t.Fatalf("created task = %+v", created.Task)
	}
	cl := e.log(created.LogID)
	if cl.Kind != "task.create" || cl.Actor != "alice" || len(cl.Before) != 0 || str(cl.After["status"]) != `"todo"` || str(cl.After["sprint"]) != "1" {
		t.Fatalf("create entry = %+v", cl)
	}

	upd := e.must("bob", func(c Ctx) (Change, error) {
		p := "Fix CI badly"
		pr := 3
		return UpdateTask(c, 1, TaskPatch{Title: &p, Priority: &pr})
	})
	ul := e.log(upd.LogID)
	if ul.Kind != "task.update" || str(ul.Before["title"]) != `"Fix CI now"` || str(ul.After["title"]) != `"Fix CI badly"` ||
		str(ul.Before["priority"]) != "2" || str(ul.After["priority"]) != "3" || len(ul.After) != 2 {
		t.Fatalf("update entry journals only what changed: %+v", ul)
	}

	mv := e.must("bob", move(1, "doing"))
	ml := e.log(mv.LogID)
	if ml.Kind != "task.move" || str(ml.Before["status"]) != `"todo"` || str(ml.After["status"]) != `"doing"` || mv.Text != "T-1 todo → doing (log #5)" {
		t.Fatalf("move: %q, entry %+v", mv.Text, ml)
	}

	// Between sprints, then a soft delete: nothing leaves the database.
	e.must("alice", func(c Ctx) (Change, error) { return CreateSprint(c, "Two", "") })
	to := int64(2)
	mvs := e.must("alice", func(c Ctx) (Change, error) { return UpdateTask(c, 1, TaskPatch{Sprint: &to}) })
	if l := e.log(mvs.LogID); str(l.Before["sprint"]) != "1" || str(l.After["sprint"]) != "2" {
		t.Fatalf("sprint move entry: %+v", l)
	}
	del := e.must("alice", func(c Ctx) (Change, error) { return DeleteTask(c, 1) })
	if l := e.log(del.LogID); l.Kind != "task.delete" || str(l.After["deleted"]) != "true" {
		t.Fatalf("delete entry: %+v", l)
	}
	if got := e.task(1); !got.Deleted || got.Title != "Fix CI badly" {
		t.Fatalf("a deleted task must stay in the database: %+v", got)
	}
	if _, err := e.do("alice", move(1, "done")); code(err) != "no_such_task" {
		t.Fatalf("a deleted task can't be changed: %v", err)
	}
}

func TestInvalidTransitionsAndCustomProcess(t *testing.T) {
	e := newEnv(t)
	e.must("a", createTask("one"))
	_, err := e.do("a", move(1, "done"))
	if code(err) != "bad_transition" || !strings.Contains(err.Error(), "doing, blocked, cancelled") {
		t.Fatalf("todo → done: %v; want bad_transition listing the allowed statuses", err)
	}
	if _, err := e.do("a", move(1, "nope")); code(err) != "bad_status" {
		t.Fatalf("unknown status: %v", err)
	}
	if e.task(1).Status != "todo" {
		t.Fatal("a refused move changed the task")
	}

	wf := store.Workflow{Statuses: []string{"todo", "shipped"}, Initial: "todo", Terminal: []string{"shipped"},
		Transitions: map[string][]string{"todo": {"shipped"}}}
	// The existing task is "todo", which the new process has: accepted.
	e.must("a", func(c Ctx) (Change, error) { return SetWorkflow(c, wf) })
	e.must("a", move(1, "shipped"))
	e.must("a", createTask("two"))
	if got := e.task(2); got.Status != "todo" {
		t.Fatalf("new task status = %q, want the process's initial status", got.Status)
	}
	if _, err := e.do("a", move(2, "doing")); code(err) != "bad_status" {
		t.Fatalf("a status of the old process: %v", err)
	}

	// Replacing the process while tasks sit in statuses it lacks is refused, with the count.
	_, err = e.do("a", func(c Ctx) (Change, error) {
		return SetWorkflow(c, store.Workflow{Statuses: []string{"todo", "closed"}, Initial: "todo", Terminal: []string{"closed"}})
	})
	if code(err) != "orphaned_statuses" || !strings.Contains(err.Error(), "shipped (1)") {
		t.Fatalf("orphaning replacement: %v", err)
	}
	for _, bad := range []store.Workflow{
		{},
		{Statuses: []string{"A"}, Initial: "A"},
		{Statuses: []string{"a", "a"}, Initial: "a"},
		{Statuses: []string{"a"}, Initial: "b"},
		{Statuses: []string{"a"}, Initial: "a", Transitions: map[string][]string{"a": {"zzz"}}},
	} {
		if _, err := e.do("a", func(c Ctx) (Change, error) { return SetWorkflow(c, bad) }); code(err) != "bad_workflow" {
			t.Fatalf("workflow %+v: %v", bad, err)
		}
	}
}

func TestOnlyOneSprintIsActiveAndClosingCarriesTheUnfinished(t *testing.T) {
	e := newEnv(t)
	e.must("a", func(c Ctx) (Change, error) { return CreateSprint(c, "one", "") })
	e.must("a", func(c Ctx) (Change, error) { return CreateSprint(c, "two", "") })
	e.must("a", func(c Ctx) (Change, error) { return StartSprint(c, 1) })
	if _, err := e.do("a", func(c Ctx) (Change, error) { return StartSprint(c, 2) }); code(err) != "sprint_active" {
		t.Fatalf("a second active sprint: %v", err)
	}
	for _, title := range []string{"open", "finished", "also open"} {
		e.must("a", func(c Ctx) (Change, error) { return CreateTask(c, TaskArgs{Title: title, Sprint: 1}) })
	}
	e.must("a", move(2, "doing"))
	e.must("a", move(2, "review"))
	e.must("a", move(2, "done"))

	ch := e.must("a", func(c Ctx) (Change, error) { return CloseSprint(c, 1, 2) })
	if !strings.Contains(ch.Text, "2 unfinished") {
		t.Fatalf("close: %s", ch.Text)
	}
	if e.task(1).Sprint != 2 || e.task(3).Sprint != 2 || e.task(2).Sprint != 1 {
		t.Fatalf("carried to S-2: %d %d, finished stays in S-1: %d", e.task(1).Sprint, e.task(3).Sprint, e.task(2).Sprint)
	}
	l := e.log(ch.LogID)
	if l.Kind != "sprint.close" || str(l.After["carried"]) != "[1,3]" || str(l.After["carry_to"]) != "2" {
		t.Fatalf("one journal entry for the whole close: %+v", l)
	}
	e.must("a", func(c Ctx) (Change, error) { return StartSprint(c, 2) }) // the slot is free again
	if _, err := e.do("a", func(c Ctx) (Change, error) { return CloseSprint(c, 1, 0) }); code(err) != "bad_transition" {
		t.Fatalf("closing a closed sprint: %v", err)
	}
	// Tasks can't be put into a closed sprint.
	to := int64(1)
	if _, err := e.do("a", func(c Ctx) (Change, error) { return UpdateTask(c, 1, TaskPatch{Sprint: &to}) }); code(err) != "sprint_closed" {
		t.Fatalf("into a closed sprint: %v", err)
	}
}

// failingLog wraps a transaction and refuses to write the journal.
type failingLog struct{ store.BoardTx }

func (failingLog) AppendLog(store.LogEntry) error { return errors.New("disk full") }

func TestAFailedJournalWriteUndoesTheChange(t *testing.T) {
	e := newEnv(t)
	e.must("a", createTask("one"))
	err := e.db.Do(chatA, func(tx store.BoardTx) error {
		_, err := MoveTask(Ctx{Tx: failingLog{tx}, Actor: "a", Now: e.now}, 1, "doing")
		return err
	})
	if err == nil {
		t.Fatal("the move succeeded without a journal entry")
	}
	if got := e.task(1).Status; got != "todo" {
		t.Fatalf("status = %q after a failed journal write; the entity change must be rolled back", got)
	}
	err = e.db.Do(chatA, func(tx store.BoardTx) error {
		_, err := CreateTask(Ctx{Tx: failingLog{tx}, Actor: "a", Now: e.now}, TaskArgs{Title: "ghost"})
		return err
	})
	if err == nil {
		t.Fatal("the create succeeded without a journal entry")
	}
	if got, ok := func() (store.Task, bool) {
		var t store.Task
		var ok bool
		e.view(chatA, func(tx store.BoardTx) error { var err error; t, ok, err = tx.GetTask(2); return err })
		return t, ok
	}(); ok {
		t.Fatalf("task %+v exists though its journal entry could not be written", got)
	}
}

func TestBoardsOfDifferentChatsAreSeparateAndSurviveReopen(t *testing.T) {
	e := newEnv(t)
	e.must("a", createTask("only in A"))
	var inB []store.Task
	e.view(chatB, func(tx store.BoardTx) error {
		var err error
		inB, err = tx.ListTasks(store.TaskFilter{Sprint: -1})
		return err
	})
	if len(inB) != 0 {
		t.Fatalf("chat B sees chat A's tasks: %+v", inB)
	}
	var next int64
	e.view(chatB, func(tx store.BoardTx) error { var err error; next, err = tx.NextTaskID(); return err })
	if next != 1 {
		t.Fatalf("ids are per chat; chat B's first task id = %d", next)
	}

	e.db.Close()
	db2, err := sqlite.Open(e.path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	var got store.Task
	if err := db2.View(chatA, func(tx store.BoardTx) error {
		var ok bool
		var err error
		got, ok, err = tx.GetTask(1)
		if err == nil && !ok {
			err = errors.New("task lost")
		}
		return err
	}); err != nil || got.Title != "only in A" {
		t.Fatalf("after a restart: %+v, err %v", got, err)
	}
}
