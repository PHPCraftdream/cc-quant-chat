package sqlite

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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

// ---- the board events of other nodes ----

type boardRule struct {
	entity string   // the entity of the journal entry ("" : any)
	fields []string // the fields the entry may set
	make   string   // the table whose one new row the event makes ("" : none)
	tables []string // the other tables whose rows it may touch
}

var allBoardFields = []string{"title", "description", "status", "sprint", "assignee", "priority", "deleted", "name", "goal", "state", "started_at", "closed_at", "carried", "carry_to", "workflow", "comment", "text", "hidden"}

var boardRules = map[string]boardRule{
	"task.create":   {"task", []string{"title", "description", "status", "sprint", "assignee", "priority", "deleted"}, "board_tasks", nil},
	"task.update":   {"task", []string{"title", "description", "assignee", "priority", "sprint"}, "", []string{"board_tasks"}},
	"task.move":     {"task", []string{"status"}, "", []string{"board_tasks"}},
	"task.delete":   {"task", []string{"deleted"}, "", []string{"board_tasks"}},
	"task.comment":  {"task", allBoardFields, "board_comments", nil},
	"sprint.create": {"sprint", []string{"name", "goal", "state", "started_at", "closed_at", "deleted"}, "board_sprints", nil},
	"sprint.update": {"sprint", []string{"name", "goal"}, "", []string{"board_sprints"}},
	"sprint.start":  {"sprint", []string{"state", "started_at"}, "", []string{"board_sprints"}},
	"sprint.close":  {"sprint", []string{"state", "closed_at", "carried", "carry_to"}, "", []string{"board_sprints", "board_tasks"}},
	"workflow.set":  {"workflow", []string{"workflow"}, "", []string{"board_workflow"}},
	"revert":        {"", allBoardFields, "", []string{"board_tasks", "board_sprints", "board_workflow", "board_comments"}},
}

func isBoardKind(kind string) bool {
	_, ok := boardRules[kind]
	return ok
}

// What the signed request says must be what the journal entry says it did.
var payloadFields = []string{"title", "description", "status", "assignee", "priority", "sprint", "name", "goal"}

func checkBoardEvent(e events.Event, ba boardAssigned, log image) error {
	rule := boardRules[e.Kind]
	entity, entityID := asStr(log.R["entity"]), asInt(log.R["entity_id"])
	if asStr(log.R["kind"]) != e.Kind {
		return fmt.Errorf("the journal entry says %q, the event %q", asStr(log.R["kind"]), e.Kind)
	}
	if rule.entity != "" && entity != rule.entity {
		return fmt.Errorf("%s acts on a %s, the entry says %q", e.Kind, rule.entity, entity)
	}
	after := rawMap(log.R["after_json"])
	for f := range after {
		if !slices.Contains(rule.fields, f) {
			return fmt.Errorf("%s may not set %q", e.Kind, f)
		}
	}
	made := 0
	for _, img := range ba.Rows {
		if img.T == "board_log" {
			continue
		}
		ok := img.T == rule.make || slices.Contains(rule.tables, img.T)
		if !ok {
			return fmt.Errorf("%s may not write the table %s", e.Kind, img.T)
		}
		if img.T == rule.make && !slices.Contains(rule.tables, img.T) {
			made++
		}
		if rule.make == "" && entity != "workflow" && (img.T == "board_tasks" || img.T == "board_sprints") && e.Kind != "sprint.close" && e.Kind != "revert" {
			if asInt(img.R["id"]) != entityID {
				return fmt.Errorf("%s touches something other than what the entry names", e.Kind)
			}
		}
	}
	if rule.make != "" && made != 1 {
		return fmt.Errorf("%s makes one row of %s, the event has %d", e.Kind, rule.make, made)
	}
	if e.Payload != "" {
		var payload map[string]any
		dec := json.NewDecoder(bytes.NewReader([]byte(e.Payload)))
		dec.UseNumber()
		if dec.Decode(&payload) == nil {
			for _, f := range payloadFields {
				pv, in1 := payload[f]
				av, in2 := after[f]
				if !in1 || !in2 {
					continue
				}
				pj, _ := json.Marshal(pv)
				if !bytes.Equal(bytes.TrimSpace(pj), bytes.TrimSpace(av)) {
					return fmt.Errorf("the request says %s=%s, the entry says %s", f, pj, av)
				}
			}
		}
	}
	return nil
}

// errMissingRef: the event names something this node does not hold yet; it may come.
var errMissingRef = errors.New("the event names something that this node does not hold yet")

// boardPlan is what a board event of another node means here.
type boardPlan struct {
	tx   *sql.Tx
	e    events.Event
	self string
	ba   boardAssigned
	// local: the number here of a thing named by the number it has in the event
	local map[string]map[int64]int64
	made  map[string]map[int64]int64 // the same, for what the event makes
}

func (p *boardPlan) name(s string) string {
	if s == "" {
		return ""
	}
	return remoteName(p.self, p.e.Origin, s)
}

// resolve gives the number here of a thing of the event; 0 stays 0.
func (p *boardPlan) resolve(kind string, id int64) (int64, error) {
	if id == 0 {
		return 0, nil
	}
	if n, ok := p.local[kind][id]; ok {
		return n, nil
	}
	if n, ok := p.made[kind][id]; ok {
		return n, nil
	}
	return 0, fmt.Errorf("the event mentions a %s (%d) that it does not name", kind, id)
}

func (p *boardPlan) setMade(kind string, rid, lid int64) {
	if p.made[kind] == nil {
		p.made[kind] = map[int64]int64{}
	}
	p.made[kind][rid] = lid
}

func nextID(tx *sql.Tx, table string) (int64, error) {
	var n int64
	err := tx.QueryRow(`SELECT COALESCE(MAX(id), 0) + 1 FROM ` + table).Scan(&n)
	return n, err
}

// remoteBoard applies an event of the board of another node. Everything it needs is looked up
// before anything is written, so that an event that cannot be applied leaves nothing behind.
func remoteBoard(tx *sql.Tx, e events.Event, self string) error {
	ba, err := parseBoard(e.Assigned)
	if err != nil {
		return fmt.Errorf("%s: assigned: %w", e.Kind, err)
	}
	var log *image
	for i := range ba.Rows {
		if ba.Rows[i].T == "board_log" {
			if log != nil {
				return fmt.Errorf("%s: more than one journal entry", e.Kind)
			}
			log = &ba.Rows[i]
		}
	}
	if log == nil {
		return fmt.Errorf("%s: no journal entry", e.Kind)
	}
	if err := checkBoardEvent(e, ba, *log); err != nil {
		return err
	}
	p := &boardPlan{tx: tx, e: e, self: self, ba: ba, local: map[string]map[int64]int64{}, made: map[string]map[int64]int64{}}
	skip, err := p.resolveRefs()
	if err != nil || skip {
		return err
	}
	return p.apply(*log)
}

// resolveRefs finds, for every thing the event names, the number here. A thing that was made by an
// event this node holds redacted, or that this node could not apply, can never be found: the event
// is then kept in the journal and not applied. A thing whose event has not arrived is waited for.
func (p *boardPlan) resolveRefs() (skip bool, err error) {
	for kind, byID := range p.ba.Refs {
		for key, ref := range byID {
			rid, perr := strconv.ParseInt(key, 10, 64)
			if perr != nil {
				return false, fmt.Errorf("a reference with the number %q", key)
			}
			lid, ok := localID(p.tx, kind, ref.Origin, ref.Seq)
			if ok {
				if p.local[kind] == nil {
					p.local[kind] = map[int64]int64{}
				}
				p.local[kind][rid] = lid
				continue
			}
			var held int
			if err := p.tx.QueryRow(`SELECT COUNT(*) FROM events WHERE origin = ? AND seq = ?`, ref.Origin, ref.Seq).Scan(&held); err != nil {
				return false, err
			}
			if held == 0 {
				return false, fmt.Errorf("%w: %s %s#%d", errMissingRef, kind, ref.Origin, ref.Seq)
			}
			return true, nil
		}
	}
	return false, nil
}

func (p *boardPlan) apply(log image) error {
	e := p.e
	// The things this event makes: the images whose number it has no name for.
	order := []string{"board_sprints", "board_tasks", "board_comments", "board_log"}
	for _, table := range order {
		kind := tableKind[table]
		for _, img := range p.ba.Rows {
			if img.T != table {
				continue
			}
			rid := asInt(img.R["id"])
			if _, known := p.local[kind][rid]; known {
				continue
			}
			if table != "board_log" && !(boardRules[e.Kind].make == table) {
				return fmt.Errorf("%s: a %s (%d) that is not named and not made by the event", e.Kind, kind, rid)
			}
			lid, err := nextID(p.tx, table)
			if err != nil {
				return err
			}
			p.setMade(kind, rid, lid)
			if err := p.insert(img, lid, rid); err != nil {
				return err
			}
			if _, err := p.tx.Exec(`INSERT INTO entity_refs (kind, id, origin, seq, num) VALUES (?, ?, ?, ?, ?)`, kind, lid, e.Origin, e.Seq, rid); err != nil {
				return err
			}
		}
	}
	// The fields the event sets: of what it made, outright; of what was there, if it is the latest.
	for _, fc := range boardChanges(p.ba.Rows) {
		if fc.kind == "workflow" {
			if err := p.setWorkflow(fc); err != nil {
				return err
			}
			continue
		}
		if lid, ok := p.made[fc.kind][fc.id]; ok {
			if err := p.stamp(fc.kind, lid, fc.field); err != nil {
				return err
			}
			continue
		}
		lid, err := p.resolve(fc.kind, fc.id)
		if err != nil {
			return err
		}
		if err := p.setField(fc, lid); err != nil {
			return err
		}
	}
	return nil
}

// insert writes a row the event makes, in the numbers of this node.
func (p *boardPlan) insert(img image, lid, rid int64) error {
	e, r := p.e, img.R
	switch img.T {
	case "board_sprints":
		_, err := p.tx.Exec(`INSERT INTO board_sprints (id, name, goal, state, created_at, started_at, closed_at, deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			lid, asStr(r["name"]), asStr(r["goal"]), asStr(r["state"]), asStr(r["created_at"]), asStr(r["started_at"]), asStr(r["closed_at"]), asInt(r["deleted"]))
		return err
	case "board_tasks":
		sprint, err := p.resolve("sprint", asInt(r["sprint"]))
		if err != nil {
			return err
		}
		_, err = p.tx.Exec(`INSERT INTO board_tasks (id, title, description, status, sprint, assignee, priority, created_by, created_at, updated_at, deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			lid, asStr(r["title"]), asStr(r["description"]), asStr(r["status"]), sprint, p.name(asStr(r["assignee"])), asInt(r["priority"]),
			p.name(asStr(r["created_by"])), asStr(r["created_at"]), asStr(r["updated_at"]), asInt(r["deleted"]))
		return err
	case "board_comments":
		task, err := p.resolve("task", asInt(r["task_id"]))
		if err != nil {
			return err
		}
		_, err = p.tx.Exec(`INSERT INTO board_comments (id, task_id, ts, author, author_owner, text, deleted, origin, lamport, author_pub, sig) VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?)`,
			lid, task, asStr(r["ts"]), p.name(asStr(r["author"])), asStr(r["text"]), asInt(r["deleted"]), e.Origin, e.Lamport, e.AuthorPub, e.Sig)
		return err
	case "board_log":
		entity, entityID := asStr(r["entity"]), asInt(r["entity_id"])
		if entity != "workflow" {
			var err error
			if entityID, err = p.resolve(entity, entityID); err != nil {
				return err
			}
		}
		reverts, err := p.resolve("log", asInt(r["reverts"]))
		if err != nil {
			return err
		}
		before, err := p.logJSON(entity, rawMap(r["before_json"]))
		if err != nil {
			return err
		}
		after, err := p.logJSON(entity, rawMap(r["after_json"]))
		if err != nil {
			return err
		}
		payload := ""
		if e.Sig != "" {
			payload = e.Payload
		}
		_, err = p.tx.Exec(`INSERT INTO board_log (id, ts, actor, actor_owner, kind, entity, entity_id, before_json, after_json, reverts, note, origin, lamport, actor_grant, author_pub, sig, payload)
			VALUES (?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?)`,
			lid, asStr(r["ts"]), p.name(asStr(r["actor"])), e.Kind, entity, entityID, before, after, reverts, asStr(r["note"]), e.Origin, e.Lamport, e.AuthorPub, e.Sig, payload)
		return err
	}
	return fmt.Errorf("an image of the unknown table %q", img.T)
}

// logJSON writes the before or after of a journal entry in the numbers of this node.
func (p *boardPlan) logJSON(entity string, m map[string]json.RawMessage) (string, error) {
	out := map[string]json.RawMessage{}
	for f, v := range m {
		out[f] = v
	}
	put := func(f string, v any) error {
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		out[f] = raw
		return nil
	}
	if v, ok := m["sprint"]; ok && entity == "task" {
		n, err := p.resolve("sprint", rawInt(v))
		if err != nil {
			return "", err
		}
		if err := put("sprint", n); err != nil {
			return "", err
		}
	}
	if v, ok := m["assignee"]; ok && entity == "task" {
		var s string
		_ = json.Unmarshal(v, &s)
		if err := put("assignee", p.name(s)); err != nil {
			return "", err
		}
	}
	if v, ok := m["carried"]; ok {
		var ids []int64
		for _, t := range rawInts(v) {
			n, err := p.resolve("task", t)
			if err != nil {
				return "", err
			}
			ids = append(ids, n)
		}
		if err := put("carried", ids); err != nil {
			return "", err
		}
	}
	if v, ok := m["comment"]; ok && entity == "task" {
		n, err := p.resolve("comment", rawInt(v))
		if err != nil {
			return "", err
		}
		if err := put("comment", n); err != nil {
			return "", err
		}
	}
	if v, ok := m["carry_to"]; ok {
		n, err := p.resolve("sprint", rawInt(v))
		if err != nil {
			return "", err
		}
		if err := put("carry_to", n); err != nil {
			return "", err
		}
	}
	return jsonText(out)
}

// newer reports whether the event's stamp is above the stored one of a field (no stamp: yes).
func (p *boardPlan) newer(kind string, id int64, field string) (bool, error) {
	var l int64
	var o string
	err := p.tx.QueryRow(`SELECT lamport, origin FROM board_stamps WHERE kind = ? AND id = ? AND field = ?`, kind, id, field).Scan(&l, &o)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return p.e.Lamport > l || (p.e.Lamport == l && p.e.Origin > o), nil
}

func (p *boardPlan) stamp(kind string, id int64, field string) error {
	_, err := p.tx.Exec(`INSERT OR REPLACE INTO board_stamps (kind, id, field, lamport, origin) VALUES (?, ?, ?, ?, ?)`, kind, id, field, p.e.Lamport, p.e.Origin)
	return err
}

// setField sets a field of something that was there before, if the event is the latest to set it.
func (p *boardPlan) setField(fc fieldChange, lid int64) error {
	if !slices.Contains(settable[fc.kind], fc.field) {
		return nil // a note of the entry, not a column
	}
	ok, err := p.newer(fc.kind, lid, fc.field)
	if err != nil || !ok {
		return err
	}
	v := fc.value
	switch {
	case fc.kind == "task" && fc.field == "sprint":
		if v, err = p.resolve("sprint", asInt(v)); err != nil {
			return err
		}
	case fc.kind == "task" && fc.field == "assignee":
		v = p.name(asStr(v))
	}
	// kind and field are from the fixed lists above
	if _, err := p.tx.Exec(`UPDATE `+kindTable[fc.kind]+` SET `+fc.field+` = ? WHERE id = ?`, v, lid); err != nil {
		return err
	}
	if fc.kind == "task" {
		if _, err := p.tx.Exec(`UPDATE board_tasks SET updated_at = MAX(updated_at, ?) WHERE id = ?`, p.e.TS, lid); err != nil {
			return err
		}
	}
	return p.stamp(fc.kind, lid, fc.field)
}

func (p *boardPlan) setWorkflow(fc fieldChange) error {
	ok, err := p.newer("workflow", 0, "workflow")
	if err != nil || !ok {
		return err
	}
	if _, err := p.tx.Exec(`INSERT OR REPLACE INTO board_workflow (id, json, updated_at) VALUES (1, ?, ?)`, asStr(fc.value), p.e.TS); err != nil {
		return err
	}
	return p.stamp("workflow", 0, "workflow")
}
