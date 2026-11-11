package board

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

func comment(id int64, text string) func(c Ctx) (Change, error) {
	return func(c Ctx) (Change, error) { return CommentTask(c, id, text) }
}

func TestCommentsAreJournaledAndTellTheRightPeople(t *testing.T) {
	e := newEnv(t)
	e.must("author", func(c Ctx) (Change, error) {
		return CreateTask(c, TaskArgs{Title: "Ship it", Assignee: "bob"})
	})
	ch := e.must("carol", comment(1, "  looks risky\r\nplease check  "))
	if len(ch.Notes) != 2 {
		t.Fatalf("notes = %+v; want the assignee and the task's author", ch.Notes)
	}
	var woken, quiet string
	for _, n := range ch.Notes {
		if n.Wake {
			woken = n.To
		} else {
			quiet = n.To
		}
	}
	if woken != "bob" || quiet != "author" {
		t.Fatalf("the assignee is woken by a comment (%q), the task's author told quietly (%q)", woken, quiet)
	}
	l := e.log(ch.LogID)
	if l.Kind != "task.comment" || str(l.After["text"]) != `"looks risky\nplease check"` || str(l.After["comment"]) != "1" {
		t.Fatalf("journal entry carries the text so the comment can be rebuilt: %+v", l)
	}

	// Nobody is told of their own comment.
	own := e.must("bob", comment(1, "on it"))
	for _, n := range own.Notes {
		if n.To == "bob" {
			t.Fatalf("the author of a comment is told of it: %+v", own.Notes)
		}
	}
	if _, err := e.do("bob", comment(1, "   ")); code(err) != "bad_args" {
		t.Fatalf("empty comment: %v", err)
	}
	if _, err := e.do("bob", comment(1, strings.Repeat("x", MaxComment+1))); code(err) != "too_large" {
		t.Fatalf("huge comment: %v", err)
	}
	if _, err := e.do("bob", comment(99, "x")); code(err) != "no_such_task" {
		t.Fatalf("comment on a missing task: %v", err)
	}
}

func TestListShowsOnlyACountAndGetShowsTheThreeLatest(t *testing.T) {
	e := newEnv(t)
	e.must("a", createTask("busy task"))
	e.must("a", createTask("quiet task"))
	for i := 1; i <= 7; i++ {
		e.must("b", comment(1, fmt.Sprintf("comment number %d", i)))
	}
	var list, get, page string
	e.view(chatA, func(tx store.BoardTx) error {
		var err error
		if list, err = List(tx, "a", ListArgs{}); err != nil {
			return err
		}
		if get, err = Get(tx, 1); err != nil {
			return err
		}
		page, err = Comments(tx, 1, 3, 0)
		return err
	})
	if !strings.Contains(list, "T-1 todo - p1 busy task [7c]") || strings.Contains(list, "comment number") || strings.Contains(list, "T-2 todo - p1 quiet task [") {
		t.Fatalf("list shows a count and no text:\n%s", list)
	}
	if !strings.Contains(get, "comments (3 of 7") || !strings.Contains(get, "comment number 7") || strings.Contains(get, "comment number 4") {
		t.Fatalf("get shows the three latest and the total:\n%s", get)
	}
	if !strings.Contains(page, "comment number 7") || !strings.Contains(page, "(older: before=5)") {
		t.Fatalf("a page of comments:\n%s", page)
	}
	var older string
	e.view(chatA, func(tx store.BoardTx) error { var err error; older, err = Comments(tx, 1, 10, 5); return err })
	if !strings.Contains(older, "comment number 4") || strings.Contains(older, "comment number 5") || strings.Contains(older, "older:") {
		t.Fatalf("paging back:\n%s", older)
	}
}

func TestRevertOfACommentHidesItAndTheRevertOfThatShowsIt(t *testing.T) {
	e := newEnv(t)
	e.must("a", createTask("t"))
	c := e.must("b", comment(1, "mistake"))
	r := e.must("a", revert(c.LogID, false))
	var count int
	e.view(chatA, func(tx store.BoardTx) error {
		m, err := tx.CountComments([]int64{1})
		count = m[1]
		return err
	})
	if count != 0 {
		t.Fatal("a reverted comment is still counted")
	}
	if l := e.log(r.LogID); l.Kind != "revert" || l.Reverts != c.LogID {
		t.Fatalf("the hiding is journaled: %+v", l)
	}
	e.must("a", revert(r.LogID, false))
	e.view(chatA, func(tx store.BoardTx) error {
		m, err := tx.CountComments([]int64{1})
		count = m[1]
		return err
	})
	if count != 1 {
		t.Fatalf("the comment must be back after reverting the revert, count %d", count)
	}
	if _, err := e.do("a", revert(c.LogID, false)); err != nil {
		t.Fatalf("hiding it again: %v", err) // the comment is shown, so the original entry applies again
	}
}

func TestEveryJournalRowHasTheNodeAndAGrowingClock(t *testing.T) {
	e := newEnv(t)
	e.must("a", createTask("one"))
	e.must("a", comment(1, "hi"))
	e.must("a", move(1, "doing"))
	var last int64
	for id := int64(1); id <= 3; id++ {
		l := e.log(id)
		if l.Origin != "00112233445566778899aabbccddeeff" || l.Lamport <= last || l.ActorGrant != "" {
			t.Fatalf("entry #%d: origin %q lamport %d (previous %d) grant %q", id, l.Origin, l.Lamport, last, l.ActorGrant)
		}
		last = l.Lamport
	}
	var cm store.Comment
	e.view(chatA, func(tx store.BoardTx) error { var err error; cm, _, err = tx.GetComment(1); return err })
	if cm.Origin != "00112233445566778899aabbccddeeff" || cm.Lamport != e.log(2).Lamport {
		t.Fatalf("a comment carries its entry's origin and clock: %+v", cm)
	}
}

// The rule for merging brokers later: the state of the board is only a cache of the journal.
func TestTaskAndCommentStateCanBeRebuiltFromTheJournal(t *testing.T) {
	e := newEnv(t)
	e.must("a", func(c Ctx) (Change, error) { return CreateSprint(c, "one", "") })
	e.must("a", func(c Ctx) (Change, error) { return StartSprint(c, 1) })
	e.must("a", func(c Ctx) (Change, error) {
		return CreateTask(c, TaskArgs{Title: "first", Description: "d1", Assignee: "bob", Priority: 2, Sprint: 1})
	})
	e.must("b", func(c Ctx) (Change, error) { return CreateTask(c, TaskArgs{Title: "second", Sprint: 1}) })
	e.must("b", func(c Ctx) (Change, error) { return CreateTask(c, TaskArgs{Title: "third"}) })
	e.must("a", move(1, "doing"))
	e.must("c", func(c Ctx) (Change, error) {
		d, pr := "changed", 3
		return UpdateTask(c, 1, TaskPatch{Description: &d, Priority: &pr})
	})
	e.must("a", comment(1, "note one"))
	hid := e.must("b", comment(1, "note two"))
	e.must("a", revert(hid.LogID, false)) // hide the second comment
	e.must("a", func(c Ctx) (Change, error) { return DeleteTask(c, 3) })
	e.must("a", move(1, "review"))
	e.must("a", move(1, "done"))
	closed := e.must("a", func(c Ctx) (Change, error) { return CloseSprint(c, 1, 0) }) // carries task 2
	e.must("a", revert(closed.LogID, false))                                           // and brings it back
	e.must("a", func(c Ctx) (Change, error) { return CloseSprint(c, 1, 0) })
	e.must("a", func(c Ctx) (Change, error) { return CreateSprint(c, "two", "") })
	back := e.must("a", func(c Ctx) (Change, error) { to := int64(2); return UpdateTask(c, 2, TaskPatch{Sprint: &to}) })
	e.must("a", revert(back.LogID, false))

	var entries []store.LogEntry
	var live map[int64]store.Task
	liveComments := map[int64]store.Comment{}
	e.view(chatA, func(tx store.BoardTx) error {
		var err error
		if entries, err = tx.LogFor("", 0, 0, LogMax); err != nil {
			return err
		}
		live = map[int64]store.Task{}
		for id := int64(1); id <= 3; id++ {
			t, ok, err := tx.GetTask(id)
			if err != nil || !ok {
				return fmt.Errorf("task %d: %v", id, err)
			}
			live[id] = t
		}
		for id := int64(1); id <= 2; id++ {
			cm, _, err := tx.GetComment(id)
			if err != nil {
				return err
			}
			liveComments[id] = cm
		}
		return nil
	})
	// LogFor is newest first; the replay wants oldest first.
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	tasks, comments := Rebuild(entries)
	if !reflect.DeepEqual(tasks, live) {
		t.Fatalf("tasks rebuilt from the journal differ from the table\nrebuilt: %+v\ntable:   %+v", tasks, live)
	}
	if !reflect.DeepEqual(comments, liveComments) {
		t.Fatalf("comments rebuilt from the journal differ from the table\nrebuilt: %+v\ntable:   %+v", comments, liveComments)
	}
}
