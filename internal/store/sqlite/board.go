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
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if err := fn(&boardTx{tx: tx, chat: chatID}); err != nil {
		return err
	}
	return tx.Commit()
}

// View runs fn on a consistent snapshot; nothing it does is kept.
func (d *DB) View(chatID string, fn func(tx store.BoardTx) error) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // reads only
	return fn(&boardTx{tx: tx, chat: chatID})
}

type boardTx struct {
	tx   *sql.Tx
	chat string
}

func (b *boardTx) nextID(table string) (int64, error) {
	var n int64
	err := b.tx.QueryRow(`SELECT COALESCE(MAX(id), 0) + 1 FROM `+table+` WHERE chat_id = ?`, b.chat).Scan(&n)
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
	t, err := scanTask(b.tx.QueryRow(`SELECT `+taskCols+` FROM board_tasks WHERE chat_id = ? AND id = ?`, b.chat, id))
	if errors.Is(err, sql.ErrNoRows) {
		return store.Task{}, false, nil
	}
	return t, err == nil, err
}

func (b *boardTx) PutTask(t store.Task) error {
	_, err := b.tx.Exec(`INSERT OR REPLACE INTO board_tasks (chat_id, `+taskCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.chat, t.ID, t.Title, t.Description, t.Status, t.Sprint, t.Assignee, t.Priority, t.CreatedBy, t.CreatedAt, t.UpdatedAt, boolInt(t.Deleted))
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
	q := `SELECT ` + taskCols + ` FROM board_tasks WHERE chat_id = ? AND deleted = 0`
	args := []any{b.chat}
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
	q := `SELECT status, count(*) FROM board_tasks WHERE chat_id = ? AND deleted = 0`
	args := []any{b.chat}
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
	s, err := scanSprint(b.tx.QueryRow(`SELECT `+sprintCols+` FROM board_sprints WHERE chat_id = ? AND id = ?`, b.chat, id))
	if errors.Is(err, sql.ErrNoRows) {
		return store.Sprint{}, false, nil
	}
	return s, err == nil, err
}

func (b *boardTx) PutSprint(s store.Sprint) error {
	_, err := b.tx.Exec(`INSERT OR REPLACE INTO board_sprints (chat_id, `+sprintCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.chat, s.ID, s.Name, s.Goal, s.State, s.CreatedAt, s.StartedAt, s.ClosedAt, boolInt(s.Deleted))
	return err
}

func (b *boardTx) ListSprints() ([]store.Sprint, error) {
	rows, err := b.tx.Query(`SELECT `+sprintCols+` FROM board_sprints WHERE chat_id = ? AND deleted = 0 ORDER BY id`, b.chat)
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
	s, err := scanSprint(b.tx.QueryRow(`SELECT `+sprintCols+` FROM board_sprints WHERE chat_id = ? AND state = 'active' AND deleted = 0`, b.chat))
	if errors.Is(err, sql.ErrNoRows) {
		return store.Sprint{}, false, nil
	}
	return s, err == nil, err
}

func (b *boardTx) GetWorkflow() (store.Workflow, bool, error) {
	var raw string
	err := b.tx.QueryRow(`SELECT json FROM board_workflow WHERE chat_id = ?`, b.chat).Scan(&raw)
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
	raw, err := json.Marshal(w)
	if err != nil {
		return err
	}
	_, err = b.tx.Exec(`INSERT OR REPLACE INTO board_workflow (chat_id, json, updated_at) VALUES (?, ?, ?)`, b.chat, string(raw), ts)
	return err
}

const logCols = `id, ts, actor, actor_owner, kind, entity, entity_id, before_json, after_json, reverts, note`

func scanLog(row interface{ Scan(...any) error }) (store.LogEntry, error) {
	var e store.LogEntry
	var owner int
	var before, after string
	if err := row.Scan(&e.ID, &e.TS, &e.Actor, &owner, &e.Kind, &e.Entity, &e.EntityID, &before, &after, &e.Reverts, &e.Note); err != nil {
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
	before, err := json.Marshal(nonNil(e.Before))
	if err != nil {
		return err
	}
	after, err := json.Marshal(nonNil(e.After))
	if err != nil {
		return err
	}
	_, err = b.tx.Exec(`INSERT INTO board_log (chat_id, `+logCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.chat, e.ID, e.TS, e.Actor, boolInt(e.ActorOwner), e.Kind, e.Entity, e.EntityID, string(before), string(after), e.Reverts, e.Note)
	return err
}

func nonNil(m map[string]json.RawMessage) map[string]json.RawMessage {
	if m == nil {
		return map[string]json.RawMessage{}
	}
	return m
}

func (b *boardTx) GetLog(id int64) (store.LogEntry, bool, error) {
	e, err := scanLog(b.tx.QueryRow(`SELECT `+logCols+` FROM board_log WHERE chat_id = ? AND id = ?`, b.chat, id))
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
	return b.queryLog(`SELECT `+logCols+` FROM board_log WHERE chat_id = ? AND entity = ? AND entity_id = ? AND id > ? ORDER BY id`,
		b.chat, entity, entityID, after)
}

func (b *boardTx) LogFor(entity string, entityID int64, before int64, limit int) ([]store.LogEntry, error) {
	q := `SELECT ` + logCols + ` FROM board_log WHERE chat_id = ?`
	args := []any{b.chat}
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
