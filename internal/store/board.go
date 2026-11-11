package store

import "encoding/json"

// The task board of a chat: sprints, tasks, a status process and an append-only journal. Unlike
// messages it is not replayed into memory: the backend answers queries, and every change runs in
// one transaction together with its journal entry.

// Task is a unit of work. Status is a name from the chat's Workflow; Sprint 0 is the backlog.
type Task struct {
	ID          int64
	Title       string
	Description string
	Status      string
	Sprint      int64
	Assignee    string
	Priority    int // 0..3
	CreatedBy   string
	CreatedAt   string // RFC 3339
	UpdatedAt   string
	Deleted     bool // soft delete: nothing is removed physically
}

// Sprint states.
const (
	SprintPlanned = "planned"
	SprintActive  = "active"
	SprintClosed  = "closed"
)

// Sprint groups tasks. At most one sprint per chat is active.
type Sprint struct {
	ID        int64
	Name      string
	Goal      string
	State     string
	CreatedAt string
	StartedAt string
	ClosedAt  string
	Deleted   bool
}

// Workflow is the state machine of task statuses, one per board.
type Workflow struct {
	Statuses    []string            `json:"statuses"`
	Initial     string              `json:"initial"`
	Terminal    []string            `json:"terminal"`
	Transitions map[string][]string `json:"transitions"`
}

// LogEntry is one line of the journal. Before and After hold only the fields that changed, as
// JSON values; Reverts is the entry a "revert" entry undoes.
type LogEntry struct {
	ID         int64
	TS         string
	Actor      string
	ActorOwner bool
	Kind       string // task.create|update|move|delete, sprint.create|update|start|close, workflow.set, revert
	Entity     string // task | sprint | workflow
	EntityID   int64
	Before     map[string]json.RawMessage
	After      map[string]json.RawMessage
	Reverts    int64
	Note       string
	Origin     string // the node that wrote the entry
	Lamport    int64  // the chat's logical clock at that write
	ActorGrant string // reserved: a grant the actor held (empty for now)
}

// Comment is a note on a task. Append-only: Deleted hides it.
type Comment struct {
	ID          int64
	TaskID      int64
	TS          string
	Author      string
	AuthorOwner bool
	Text        string
	Deleted     bool
	Origin      string
	Lamport     int64
}

// TaskFilter selects tasks for a listing. Sprint: -1 any, 0 backlog, n that sprint.
type TaskFilter struct {
	Sprint      int64
	Statuses    []string // empty: any
	NotStatuses []string // excluded
	Assignee    string
	Limit       int // 0: no limit
}

// BoardTx is the board of one chat inside a transaction. Reads see the transaction's own writes.
type BoardTx interface {
	NextTaskID() (int64, error)
	GetTask(id int64) (Task, bool, error)
	PutTask(t Task) error // insert or replace by ID
	ListTasks(f TaskFilter) ([]Task, error)
	CountByStatus(sprint int64) (map[string]int, error) // non-deleted tasks of a sprint (-1: all)

	NextSprintID() (int64, error)
	GetSprint(id int64) (Sprint, bool, error)
	PutSprint(s Sprint) error
	ListSprints() ([]Sprint, error)
	ActiveSprint() (Sprint, bool, error)

	GetWorkflow() (Workflow, bool, error)
	PutWorkflow(w Workflow, ts string) error

	// NextLamport advances and returns the chat's logical clock.
	NextLamport() (int64, error)

	NextCommentID() (int64, error)
	PutComment(c Comment) error // insert or replace by ID
	GetComment(id int64) (Comment, bool, error)
	// ListComments: live comments of a task, newest first; before 0 means from the newest.
	ListComments(taskID, before int64, limit int) ([]Comment, error)
	// CountComments: live comments per task, for the given tasks.
	CountComments(taskIDs []int64) (map[int64]int, error)

	NextLogID() (int64, error)
	AppendLog(e LogEntry) error
	GetLog(id int64) (LogEntry, bool, error)
	// LogsAfter: entries with ID > after for one entity, oldest first.
	LogsAfter(entity string, entityID, after int64) ([]LogEntry, error)
	// LogFor: newest first; entity "" means the whole board; before 0 means from the newest.
	LogFor(entity string, entityID int64, before int64, limit int) ([]LogEntry, error)
}

// Board is the optional part of a backend that keeps task boards. Do runs fn in a transaction
// for one chat: fn's error rolls everything back. View is the same for reads.
type Board interface {
	Do(chatID string, fn func(tx BoardTx) error) error
	View(chatID string, fn func(tx BoardTx) error) error
}
