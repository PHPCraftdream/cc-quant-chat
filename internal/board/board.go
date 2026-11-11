// Package board is the task board of a chat: tasks, sprints, a status process (workflow) and an
// append-only journal. Every function runs inside one store transaction (store.Board.Do): the
// change to an entity and its journal entry are written together or not at all.
//
// Nothing here knows about connections or chats' members; the broker validates who may act and
// passes the actor in.
package board

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// Limits.
const (
	MaxTitle       = 200
	MaxDescription = 16 << 10
	MaxSprintName  = 80
	MaxGoal        = 500
	MaxStatuses    = 16
	MaxPriority    = 3
	ListLines      = 50
	TitleInList    = 80
	LogDefault     = 20
	LogMax         = 100
)

// Ctx is who acts, when, and on which transaction.
type Ctx struct {
	Tx     store.BoardTx
	Actor  string
	Owner  bool   // the actor is the verified owner
	Grant  string // reserved: a grant the actor holds (empty for now)
	Origin string // this node, written on every journal row
	Now    time.Time
}

func (c Ctx) ts() string { return c.Now.UTC().Format(time.RFC3339) }

// Change is the outcome of a modifying call.
type Change struct {
	Text   string // one line for the actor
	LogID  int64
	Task   store.Task // the task after the change (zero for sprint and workflow changes)
	Fields []string   // task fields that changed
	// Notes, when set, say exactly who is told about the change; empty means the broker applies
	// its default (the task's assignee, if somebody else made the change).
	Notes []Note
}

// Note is a message to one member about a board change.
type Note struct {
	To   string
	Text string
	Wake bool
}

// DefaultWorkflow is the process of a board that never set its own.
func DefaultWorkflow() store.Workflow {
	return store.Workflow{
		Statuses: []string{"todo", "doing", "review", "done", "blocked", "cancelled"},
		Initial:  "todo",
		Terminal: []string{"done", "cancelled"},
		Transitions: map[string][]string{
			"todo":      {"doing", "blocked", "cancelled"},
			"doing":     {"review", "blocked", "todo", "cancelled"},
			"review":    {"done", "doing"},
			"blocked":   {"todo", "doing", "cancelled"},
			"done":      {"doing"},
			"cancelled": {"todo"},
		},
	}
}

func workflow(tx store.BoardTx) (store.Workflow, error) {
	w, ok, err := tx.GetWorkflow()
	if err != nil {
		return w, err
	}
	if !ok {
		return DefaultWorkflow(), nil
	}
	return w, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ---- journal ----

func (c Ctx) log(kind, entity string, id int64, before, after map[string]json.RawMessage, reverts int64) (int64, error) {
	n, _, err := c.logL(kind, entity, id, before, after, reverts)
	return n, err
}

// logL also returns the logical clock value the entry was written with.
func (c Ctx) logL(kind, entity string, id int64, before, after map[string]json.RawMessage, reverts int64) (int64, int64, error) {
	n, err := c.Tx.NextLogID()
	if err != nil {
		return 0, 0, err
	}
	lamport, err := c.Tx.NextLamport()
	if err != nil {
		return 0, 0, err
	}
	err = c.Tx.AppendLog(store.LogEntry{ID: n, TS: c.ts(), Actor: c.Actor, ActorOwner: c.Owner, Kind: kind,
		Entity: entity, EntityID: id, Before: before, After: after, Reverts: reverts,
		Origin: c.Origin, Lamport: lamport, ActorGrant: c.Grant})
	return n, lamport, err
}

func raw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // the values here are strings, numbers, bools and slices of them
	}
	return b
}

func sameJSON(a, b json.RawMessage) bool { return string(a) == string(b) }

// ---- fields of a task ----

var taskFieldNames = []string{"title", "description", "status", "sprint", "assignee", "priority", "deleted"}

func taskField(t store.Task, f string) json.RawMessage {
	switch f {
	case "title":
		return raw(t.Title)
	case "description":
		return raw(t.Description)
	case "status":
		return raw(t.Status)
	case "sprint":
		return raw(t.Sprint)
	case "assignee":
		return raw(t.Assignee)
	case "priority":
		return raw(t.Priority)
	case "deleted":
		return raw(t.Deleted)
	}
	return nil
}

func setTaskField(t *store.Task, f string, v json.RawMessage) error {
	var err error
	switch f {
	case "title":
		err = json.Unmarshal(v, &t.Title)
	case "description":
		err = json.Unmarshal(v, &t.Description)
	case "status":
		err = json.Unmarshal(v, &t.Status)
	case "sprint":
		err = json.Unmarshal(v, &t.Sprint)
	case "assignee":
		err = json.Unmarshal(v, &t.Assignee)
	case "priority":
		err = json.Unmarshal(v, &t.Priority)
	case "deleted":
		err = json.Unmarshal(v, &t.Deleted)
	default:
		err = fmt.Errorf("unknown task field %q", f)
	}
	return err
}

func taskFields(t store.Task, names ...string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, f := range names {
		out[f] = taskField(t, f)
	}
	return out
}

// ---- ids and text ----

// TaskRef formats a task id as people write it: T-12.
func TaskRef(id int64) string { return fmt.Sprintf("T-%d", id) }

// SprintRef formats a sprint id: S-3.
func SprintRef(id int64) string { return fmt.Sprintf("S-%d", id) }

// oneLine makes a title or name a single printable line.
func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// clip shortens s to n runes with an ellipsis.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func errNoTask(id int64) error {
	return proto.Errf("no_such_task", "there is no task %s", TaskRef(id))
}

func errNoSprint(id int64) error {
	return proto.Errf("no_such_sprint", "there is no sprint %s", SprintRef(id))
}

// loadTask returns a live (not deleted) task.
func loadTask(tx store.BoardTx, id int64) (store.Task, error) {
	t, ok, err := tx.GetTask(id)
	if err != nil {
		return t, err
	}
	if !ok || t.Deleted {
		return t, errNoTask(id)
	}
	return t, nil
}

// checkSprintTarget: a sprint tasks may be put in (0 = backlog).
func checkSprintTarget(tx store.BoardTx, id int64) error {
	if id == 0 {
		return nil
	}
	s, ok, err := tx.GetSprint(id)
	if err != nil {
		return err
	}
	if !ok || s.Deleted {
		return errNoSprint(id)
	}
	if s.State == store.SprintClosed {
		return proto.Errf("sprint_closed", "%s is closed; tasks can't be put in it", SprintRef(id))
	}
	return nil
}
