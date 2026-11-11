package sqlite

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

var _ store.Board = (*DB)(nil)

// Do runs fn in one transaction on the board of a chat; an error from fn rolls it all back.
func (d *DB) Do(chatID string, fn func(tx store.BoardTx) error) error {
	c, err := d.chat(chatID, "", true)
	if err != nil {
		return err
	}
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if err := fn(&boardTx{tx: tx, rec: recorder{c: c, tx: tx}}); err != nil {
		return err
	}
	return tx.Commit()
}

// View runs fn on a consistent snapshot; nothing it does is kept.
func (d *DB) View(chatID string, fn func(tx store.BoardTx) error) error {
	c, err := d.chat(chatID, "", true)
	if err != nil {
		return err
	}
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // reads only
	return fn(&boardTx{tx: tx, rec: recorder{c: c, tx: tx}})
}

type boardTx struct {
	tx  *sql.Tx
	rec recorder
}

func (b *boardTx) RecordEvent(in store.EventIn) error { return b.rec.RecordEvent(in) }

func (b *boardTx) nextID(table string) (int64, error) {
	var n int64
	err := b.tx.QueryRow(`SELECT COALESCE(MAX(id), 0) + 1 FROM ` + table).Scan(&n)
	return n, err
}

func (b *boardTx) NextTaskID() (int64, error)   { return b.nextID("board_tasks") }
func (b *boardTx) NextSprintID() (int64, error) { return b.nextID("board_sprints") }
func (b *boardTx) NextLogID() (int64, error)    { return b.nextID("board_log") }

const taskCols = `id, title, description, status, sprint, assignee, priority, created_by, created_at, updated_at, deleted`

func scanTask(row interface{ Scan(...any) error }) (store.Task, error) {
	var t store.Task
	var del int
	err := row.Scan(&t.ID, &t.Title, &t.Description, &t.Status, &t.Sprint, &t.Assignee, &t.Priority, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt, &del)
	t.Deleted = del != 0
	return t, err
}

func (b *boardTx) GetTask(id int64) (store.Task, bool, error) {
	t, err := scanTask(b.tx.QueryRow(`SELECT `+taskCols+` FROM board_tasks WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return store.Task{}, false, nil
	}
	return t, err == nil, err
}

func (b *boardTx) PutTask(t store.Task) error {
	b.rec.touch("board_tasks", t.ID)
	_, err := b.tx.Exec(`INSERT OR REPLACE INTO board_tasks (`+taskCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Title, t.Description, t.Status, t.Sprint, t.Assignee, t.Priority, t.CreatedBy, t.CreatedAt, t.UpdatedAt, boolInt(t.Deleted))
	return err
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func (b *boardTx) ListTasks(f store.TaskFilter) ([]store.Task, error) {
	q := `SELECT ` + taskCols + ` FROM board_tasks WHERE deleted = 0`
	var args []any
	if f.Sprint >= 0 {
		q += ` AND sprint = ?`
		args = append(args, f.Sprint)
	}
	if len(f.Statuses) > 0 {
		q += ` AND status IN (` + placeholders(len(f.Statuses)) + `)`
		for _, s := range f.Statuses {
			args = append(args, s)
		}
	}
	if len(f.NotStatuses) > 0 {
		q += ` AND status NOT IN (` + placeholders(len(f.NotStatuses)) + `)`
		for _, s := range f.NotStatuses {
			args = append(args, s)
		}
	}
	if f.Assignee != "" {
		q += ` AND assignee = ?`
		args = append(args, f.Assignee)
	}
	q += ` ORDER BY priority DESC, id`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	rows, err := b.tx.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (b *boardTx) CountByStatus(sprint int64) (map[string]int, error) {
	q := `SELECT status, count(*) FROM board_tasks WHERE deleted = 0`
	var args []any
	if sprint >= 0 {
		q += ` AND sprint = ?`
		args = append(args, sprint)
	}
	rows, err := b.tx.Query(q+` GROUP BY status`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[s] = n
	}
	return out, rows.Err()
}

const sprintCols = `id, name, goal, state, created_at, started_at, closed_at, deleted`

func scanSprint(row interface{ Scan(...any) error }) (store.Sprint, error) {
	var s store.Sprint
	var del int
	err := row.Scan(&s.ID, &s.Name, &s.Goal, &s.State, &s.CreatedAt, &s.StartedAt, &s.ClosedAt, &del)
	s.Deleted = del != 0
	return s, err
}

func (b *boardTx) GetSprint(id int64) (store.Sprint, bool, error) {
	s, err := scanSprint(b.tx.QueryRow(`SELECT `+sprintCols+` FROM board_sprints WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return store.Sprint{}, false, nil
	}
	return s, err == nil, err
}

func (b *boardTx) PutSprint(s store.Sprint) error {
	b.rec.touch("board_sprints", s.ID)
	_, err := b.tx.Exec(`INSERT OR REPLACE INTO board_sprints (`+sprintCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.Name, s.Goal, s.State, s.CreatedAt, s.StartedAt, s.ClosedAt, boolInt(s.Deleted))
	return err
}

func (b *boardTx) ListSprints() ([]store.Sprint, error) {
	rows, err := b.tx.Query(`SELECT ` + sprintCols + ` FROM board_sprints WHERE deleted = 0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Sprint
	for rows.Next() {
		s, err := scanSprint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (b *boardTx) ActiveSprint() (store.Sprint, bool, error) {
	s, err := scanSprint(b.tx.QueryRow(`SELECT ` + sprintCols + ` FROM board_sprints WHERE state = 'active' AND deleted = 0`))
	if errors.Is(err, sql.ErrNoRows) {
		return store.Sprint{}, false, nil
	}
	return s, err == nil, err
}

func (b *boardTx) GetWorkflow() (store.Workflow, bool, error) {
	var raw string
	err := b.tx.QueryRow(`SELECT json FROM board_workflow WHERE id = 1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Workflow{}, false, nil
	}
	if err != nil {
		return store.Workflow{}, false, err
	}
	var w store.Workflow
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		return store.Workflow{}, false, err
	}
	return w, true, nil
}

func (b *boardTx) PutWorkflow(w store.Workflow, ts string) error {
	b.rec.touch("board_workflow")
	raw, err := json.Marshal(w)
	if err != nil {
		return err
	}
	_, err = b.tx.Exec(`INSERT OR REPLACE INTO board_workflow (id, json, updated_at) VALUES (1, ?, ?)`, string(raw), ts)
	return err
}

const logCols = `id, ts, actor, actor_owner, kind, entity, entity_id, before_json, after_json, reverts, note, origin, lamport, actor_grant, author_pub, sig, payload`

func scanLog(row interface{ Scan(...any) error }) (store.LogEntry, error) {
	var e store.LogEntry
	var owner int
	var before, after string
	if err := row.Scan(&e.ID, &e.TS, &e.Actor, &owner, &e.Kind, &e.Entity, &e.EntityID, &before, &after, &e.Reverts, &e.Note, &e.Origin, &e.Lamport, &e.ActorGrant, &e.AuthorPub, &e.Sig, &e.Payload); err != nil {
		return e, err
	}
	e.ActorOwner = owner != 0
	if err := json.Unmarshal([]byte(before), &e.Before); err != nil {
		return e, err
	}
	if err := json.Unmarshal([]byte(after), &e.After); err != nil {
		return e, err
	}
	return e, nil
}

func (b *boardTx) AppendLog(e store.LogEntry) error {
	b.rec.touch("board_log", e.ID)
	before, err := json.Marshal(nonNil(e.Before))
	if err != nil {
		return err
	}
	after, err := json.Marshal(nonNil(e.After))
	if err != nil {
		return err
	}
	_, err = b.tx.Exec(`INSERT INTO board_log (`+logCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.TS, e.Actor, boolInt(e.ActorOwner), e.Kind, e.Entity, e.EntityID, string(before), string(after), e.Reverts, e.Note,
		e.Origin, e.Lamport, e.ActorGrant, e.AuthorPub, e.Sig, e.Payload)
	return err
}

func nonNil(m map[string]json.RawMessage) map[string]json.RawMessage {
	if m == nil {
		return map[string]json.RawMessage{}
	}
	return m
}

func (b *boardTx) GetLog(id int64) (store.LogEntry, bool, error) {
	e, err := scanLog(b.tx.QueryRow(`SELECT `+logCols+` FROM board_log WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return store.LogEntry{}, false, nil
	}
	return e, err == nil, err
}

func (b *boardTx) queryLog(q string, args ...any) ([]store.LogEntry, error) {
	rows, err := b.tx.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.LogEntry
	for rows.Next() {
		e, err := scanLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (b *boardTx) LogsAfter(entity string, entityID, after int64) ([]store.LogEntry, error) {
	return b.queryLog(`SELECT `+logCols+` FROM board_log WHERE entity = ? AND entity_id = ? AND id > ? ORDER BY id`,
		entity, entityID, after)
}

func (b *boardTx) LogFor(entity string, entityID int64, before int64, limit int) ([]store.LogEntry, error) {
	q := `SELECT ` + logCols + ` FROM board_log WHERE 1 = 1`
	var args []any
	if entity != "" {
		q += ` AND entity = ? AND entity_id = ?`
		args = append(args, entity, entityID)
	}
	if before > 0 {
		q += ` AND id < ?`
		args = append(args, before)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	return b.queryLog(q, args...)
}

// NextLamport advances the chat's logical clock and returns the new value.
func (b *boardTx) NextLamport() (int64, error) {
	if _, err := b.tx.Exec(`UPDATE chat SET lamport = lamport + 1 WHERE id = 1`); err != nil {
		return 0, err
	}
	var n int64
	err := b.tx.QueryRow(`SELECT lamport FROM chat WHERE id = 1`).Scan(&n)
	if b.rec.lamport == 0 {
		b.rec.lamport = n // the event carries the clock of the change's first write
	}
	return n, err
}

func (b *boardTx) NextCommentID() (int64, error) { return b.nextID("board_comments") }

const commentCols = `id, task_id, ts, author, author_owner, text, deleted, origin, lamport, author_pub, sig`

func scanComment(row interface{ Scan(...any) error }) (store.Comment, error) {
	var c store.Comment
	var owner, del int
	err := row.Scan(&c.ID, &c.TaskID, &c.TS, &c.Author, &owner, &c.Text, &del, &c.Origin, &c.Lamport, &c.AuthorPub, &c.Sig)
	c.AuthorOwner, c.Deleted = owner != 0, del != 0
	return c, err
}

func (b *boardTx) PutComment(c store.Comment) error {
	b.rec.touch("board_comments", c.ID)
	_, err := b.tx.Exec(`INSERT OR REPLACE INTO board_comments (`+commentCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.TaskID, c.TS, c.Author, boolInt(c.AuthorOwner), c.Text, boolInt(c.Deleted), c.Origin, c.Lamport, c.AuthorPub, c.Sig)
	return err
}

func (b *boardTx) GetComment(id int64) (store.Comment, bool, error) {
	c, err := scanComment(b.tx.QueryRow(`SELECT `+commentCols+` FROM board_comments WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return store.Comment{}, false, nil
	}
	return c, err == nil, err
}

func (b *boardTx) ListComments(taskID, before int64, limit int) ([]store.Comment, error) {
	q := `SELECT ` + commentCols + ` FROM board_comments WHERE task_id = ? AND deleted = 0`
	args := []any{taskID}
	if before > 0 {
		q += ` AND id < ?`
		args = append(args, before)
	}
	rows, err := b.tx.Query(q+` ORDER BY id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Comment
	for rows.Next() {
		c, err := scanComment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (b *boardTx) CountComments(taskIDs []int64) (map[int64]int, error) {
	out := map[int64]int{}
	if len(taskIDs) == 0 {
		return out, nil
	}
	var args []any
	for _, id := range taskIDs {
		args = append(args, id)
	}
	rows, err := b.tx.Query(`SELECT task_id, count(*) FROM board_comments WHERE deleted = 0 AND task_id IN (`+placeholders(len(taskIDs))+`) GROUP BY task_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}
