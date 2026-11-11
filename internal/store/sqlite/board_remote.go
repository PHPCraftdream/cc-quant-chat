package sqlite

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
)

// The board of a chat when other nodes write to it (docs/design-federation.md §4).
//
// Every task, sprint, comment and journal row has a global name: (origin, seq) of the event that
// made it (entity_refs). An event of the board carries the global names of the things it mentions
// (assigned.refs, in the numbers of its own node), and the receiver turns them into its numbers. A
// field of a task, a sprint, a comment or the workflow is last-writer-wins by (lamport, origin)
// (board_stamps), so two concurrent edits of one field end the same on every node, whatever the order
// they arrive in. What an event claims to have done (the images of rows and the before/after of the
// journal entry) is checked against the request that the author signed before it is applied.

var tableKind = map[string]string{"board_tasks": "task", "board_sprints": "sprint", "board_comments": "comment", "board_log": "log"}

var kindTable = map[string]string{"task": "board_tasks", "sprint": "board_sprints", "comment": "board_comments", "log": "board_log"}

// The columns of each kind that may be set by a later event, as the column of the table.
var settable = map[string][]string{
	"task":    {"title", "description", "status", "sprint", "assignee", "priority", "deleted"},
	"sprint":  {"name", "goal", "state", "started_at", "closed_at", "deleted"},
	"comment": {"deleted"},
}

type boardAssigned struct {
	Rows []image                        `json:"rows"`
	Refs map[string]map[string]eventRef `json:"refs,omitempty"`
}

func parseBoard(assigned string) (boardAssigned, error) {
	var a boardAssigned
	if assigned == "" || assigned == "{}" {
		return a, nil
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(assigned)))
	dec.UseNumber()
	return a, dec.Decode(&a)
}

func asInt(v any) int64 {
	switch n := v.(type) {
	case json.Number:
		i, _ := n.Int64()
		return i
	case int64:
		return n
	case float64:
		return int64(n)
	case int:
		return int64(n)
	}
	return 0
}

func asStr(v any) string { s, _ := v.(string); return s }

// rawMap reads the before_json/after_json of a journal row.
func rawMap(v any) map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	_ = json.Unmarshal([]byte(asStr(v)), &m)
	return m
}

func rawInt(r json.RawMessage) int64 {
	var n int64
	_ = json.Unmarshal(r, &n)
	return n
}

func rawInts(r json.RawMessage) []int64 {
	var l []int64
	_ = json.Unmarshal(r, &l)
	return l
}

// fieldChange is one field of one thing that an event sets, in the numbers of the event.
type fieldChange struct {
	kind  string // task | sprint | comment | workflow
	id    int64
	field string
	value any // string, int64 or float64
}

// jsonScalar reads a JSON value of a field: strings stay strings, numbers become numbers, true and
// false become 1 and 0 (how the tables hold them).
func jsonScalar(r json.RawMessage) any {
	dec := json.NewDecoder(bytes.NewReader(r))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return nil
	}
	switch x := v.(type) {
	case bool:
		if x {
			return int64(1)
		}
		return int64(0)
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		f, _ := x.Float64()
		return f
	}
	return v
}

// changesOf lists what the images of a board event set, field by field: the fields of the journal
// entry's `after`, the sprint of the tasks a closed sprint carried on, the hidden flag of a comment,
// the workflow as a whole.
func boardChanges(imgs []image) []fieldChange {
	var out []fieldChange
	for _, img := range imgs {
		switch img.T {
		case "board_log":
			entity, id := asStr(img.R["entity"]), asInt(img.R["entity_id"])
			after := rawMap(img.R["after_json"])
			if entity == "task" || entity == "sprint" {
				for _, f := range settable[entity] {
					if v, ok := after[f]; ok {
						out = append(out, fieldChange{entity, id, f, jsonScalar(v)})
					}
				}
			}
			if entity == "sprint" {
				carry := rawInt(after["carry_to"])
				for _, t := range rawInts(after["carried"]) {
					out = append(out, fieldChange{"task", t, "sprint", carry})
				}
			}
		case "board_comments":
			out = append(out, fieldChange{"comment", asInt(img.R["id"]), "deleted", asInt(img.R["deleted"])})
		case "board_workflow":
			out = append(out, fieldChange{"workflow", 0, "workflow", asStr(img.R["json"])})
		}
	}
	return out
}

// mentioned lists the things an event's images mention by number: what must have a global name for
// the event to mean the same elsewhere.
func mentioned(imgs []image) map[string]map[int64]bool {
	out := map[string]map[int64]bool{}
	add := func(kind string, id int64) {
		if id == 0 {
			return
		}
		if out[kind] == nil {
			out[kind] = map[int64]bool{}
		}
		out[kind][id] = true
	}
	for _, img := range imgs {
		switch img.T {
		case "board_tasks":
			add("task", asInt(img.R["id"]))
			add("sprint", asInt(img.R["sprint"]))
		case "board_sprints":
			add("sprint", asInt(img.R["id"]))
		case "board_comments":
			add("comment", asInt(img.R["id"]))
			add("task", asInt(img.R["task_id"]))
		case "board_log":
			entity, id := asStr(img.R["entity"]), asInt(img.R["entity_id"])
			add(entity, id)
			add("log", asInt(img.R["reverts"]))
			for _, m := range []map[string]json.RawMessage{rawMap(img.R["before_json"]), rawMap(img.R["after_json"])} {
				if entity == "task" {
					add("comment", rawInt(m["comment"]))
					add("sprint", rawInt(m["sprint"]))
				}
				for _, t := range rawInts(m["carried"]) {
					add("task", t)
				}
				if entity == "sprint" {
					add("sprint", rawInt(m["carry_to"]))
				}
			}
		}
	}
	return out
}

// boardRefs gives the global names of the things the images mention, for the event that is about to
// be written. What has no name yet is made by this event and gets it from the event.
func boardRefs(tx *sql.Tx, imgs []image) (map[string]map[string]eventRef, error) {
	out := map[string]map[string]eventRef{}
	for kind, ids := range mentioned(imgs) {
		for id := range ids {
			ref, err := refOf(tx, kind, id)
			if err != nil {
				return nil, err
			}
			if ref == nil {
				continue
			}
			if out[kind] == nil {
				out[kind] = map[string]eventRef{}
			}
			out[kind][strconv.FormatInt(id, 10)] = *ref
		}
	}
	return out, nil
}

// noteBoard records, for an event of this node, what its images made and set: the global names of
// the things it made and the stamps of the fields it set.
func noteBoard(tx *sql.Tx, e events.Event, imgs []image) error {
	for _, img := range imgs {
		kind := tableKind[img.T]
		id := asInt(img.R["id"])
		if kind == "" || id == 0 {
			continue
		}
		var have int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM entity_refs WHERE kind = ? AND id = ?`, kind, id).Scan(&have); err != nil {
			return err
		}
		if have == 0 {
			if _, err := tx.Exec(`INSERT OR REPLACE INTO entity_refs (kind, id, origin, seq, num) VALUES (?, ?, ?, ?, ?)`, kind, id, e.Origin, e.Seq, id); err != nil {
				return err
			}
		}
	}
	for _, fc := range boardChanges(imgs) {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO board_stamps (kind, id, field, lamport, origin) VALUES (?, ?, ?, ?, ?)`,
			fc.kind, fc.id, fc.field, e.Lamport, e.Origin); err != nil {
			return err
		}
	}
	return nil
}

// deriveBoardRefs does noteBoard for the board events of a chat written before the names existed.
func deriveBoardRefs(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT origin, seq, lamport, assigned FROM events
		WHERE remote = 0 AND hidden = '' AND (kind LIKE 'task.%' OR kind LIKE 'sprint.%' OR kind LIKE 'workflow.%' OR kind = 'revert' OR kind = 'import.board')
		ORDER BY ord`)
	if err != nil {
		return err
	}
	var evs []events.Event
	for rows.Next() {
		var e events.Event
		if err := rows.Scan(&e.Origin, &e.Seq, &e.Lamport, &e.Assigned); err != nil {
			rows.Close()
			return err
		}
		evs = append(evs, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, e := range evs {
		a, err := parseBoard(e.Assigned)
		if err != nil {
			return fmt.Errorf("event %s#%d: assigned: %w", e.Origin, e.Seq, err)
		}
		if err := noteBoard(tx, e, a.Rows); err != nil {
			return err
		}
	}
	return nil
}
