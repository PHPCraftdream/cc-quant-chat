package sqlite

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
)

// What a node may claim about an event it passes on, beyond the shape of the event (see
// docs/design-federation.md section 4): who wrote it, that a request the author signed says what the
// entry says, and that a revert is the undoing of the entry it names and nothing else.

// checkAuthor: a nick whose node registered a key for it is only taken as the author of events that
// are signed by that key. A node cannot speak for such a nick without the key (the claim "this is the
// owner" does not excuse it: the owner key of another node is not known here). A nick without a key
// has the word of its node.
func checkAuthor(tx *sql.Tx, e events.Event, actor string) error {
	if actor == "" {
		return nil
	}
	var pub string
	err := tx.QueryRow(`SELECT pub FROM remote_nicks WHERE origin = ? AND nick = ?`, e.Origin, actor).Scan(&pub)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && pub == "") {
		return nil
	}
	if err != nil {
		return err
	}
	if e.Sig == "" {
		return fmt.Errorf("%s has a key at its node: its events must be signed with it", actor)
	}
	if e.AuthorPub != pub {
		return fmt.Errorf("%s: signed by a key that is not the one registered for the nick", actor)
	}
	return nil
}

func sameRaw(a, b json.RawMessage) bool {
	var x, y any
	da, db := json.NewDecoder(bytes.NewReader(a)), json.NewDecoder(bytes.NewReader(b))
	da.UseNumber()
	db.UseNumber()
	if da.Decode(&x) != nil || db.Decode(&y) != nil {
		return bytes.Equal(a, b)
	}
	return reflect.DeepEqual(x, y)
}

func rawJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// The values a creation starts a field with when the request does not give it.
var createDefaults = map[string]map[string]any{
	"task.create":   {"description": "", "sprint": 0, "assignee": "", "priority": 0, "deleted": false},
	"sprint.create": {"goal": "", "state": "planned", "started_at": "", "closed_at": "", "deleted": false},
}

// strictPayload checks, for an event that the author signed, that the entry says nothing that the
// signed request does not: every field of the entry that a request can carry is in the request and
// equal to it (for a creation: or has the value a creation starts with), and the fields that the
// node computes (state, times) are what the operation sets.
func strictPayload(kind string, payload map[string]any, after map[string]json.RawMessage) error {
	if kind == "revert" {
		return nil // its fields come from the entry it undoes (checkRevert), not from the request
	}
	for f, av := range after {
		if !slices.Contains(payloadFields, f) {
			continue
		}
		if pv, in := payload[f]; in {
			if !sameRaw(rawJSON(pv), av) {
				return fmt.Errorf("the request says %s=%v, the entry says %s", f, pv, av)
			}
			continue
		}
		if dv, has := createDefaults[kind][f]; has && sameRaw(rawJSON(dv), av) {
			continue
		}
		if kind == "task.create" && f == "status" {
			continue // the first status of the board
		}
		return fmt.Errorf("the entry sets %s, which the signed request does not carry", f)
	}
	switch kind {
	case "task.create":
		if _, ok := payload["title"]; !ok {
			return errors.New("a task is created with a title")
		}
	case "sprint.create":
		if _, ok := payload["name"]; !ok {
			return errors.New("a sprint is created with a name")
		}
	case "task.delete":
		if len(after) != 1 || !sameRaw(after["deleted"], rawJSON(true)) {
			return errors.New("a deletion sets deleted=true and nothing else")
		}
	case "sprint.start":
		if !sameRaw(after["state"], rawJSON("active")) {
			return errors.New("a sprint is started into the state active")
		}
	case "sprint.close":
		if !sameRaw(after["state"], rawJSON("closed")) {
			return errors.New("a sprint is closed into the state closed")
		}
	}
	return nil
}

// checkRevert: a revert undoes the entry it names. Its fields are what that entry had before, for the
// fields that entry set (a creation undone is a hiding), and nothing the entry did not touch. It runs
// after the references are resolved, in the numbers of this node.
func (p *boardPlan) checkRevert(log image) error {
	rid := asInt(log.R["reverts"])
	lid, ok := p.local["log"][rid]
	if rid == 0 || !ok {
		return errors.New("a revert names the entry it undoes")
	}
	var kind, entity, before, after string
	var entityID int64
	if err := p.tx.QueryRow(`SELECT kind, entity, entity_id, before_json, after_json FROM board_log WHERE id = ?`, lid).Scan(&kind, &entity, &entityID, &before, &after); err != nil {
		return fmt.Errorf("the entry that the revert names: %w", err)
	}
	ev := asStr(log.R["entity"])
	if ev != entity {
		return fmt.Errorf("a revert of a %s entry acts on a %s", entity, ev)
	}
	if ev != "workflow" {
		n, err := p.resolve(ev, asInt(log.R["entity_id"]))
		if err != nil || n != entityID {
			return errors.New("a revert acts on the thing its entry acted on")
		}
	}
	got, err := p.translated(ev, rawMap(log.R["after_json"]))
	if err != nil {
		return err
	}
	old, oldAfter := rawMap(before), rawMap(after)
	if _, isComment := oldAfter["comment"]; isComment && ev == "task" {
		target := rawJSON(true)
		if v, has := old["hidden"]; has {
			target = v
		}
		if len(got) != 2 || !sameRaw(got["comment"], oldAfter["comment"]) || !sameRaw(got["hidden"], target) {
			return errors.New("a revert of a comment hides or shows that comment")
		}
		return nil
	}
	if kind == "task.create" || kind == "sprint.create" {
		if len(got) != 1 || !sameRaw(got["deleted"], rawJSON(true)) {
			return errors.New("the undoing of a creation hides what it made")
		}
		return nil
	}
	for f, v := range got {
		switch f {
		case "carried":
			if !sameRaw(v, oldAfter["carried"]) {
				return errors.New("a revert carries the tasks its entry carried")
			}
		case "carry_to":
			if !sameRaw(v, old["carry_to"]) {
				return errors.New("a revert carries the tasks back to where they were")
			}
		default:
			if _, set := oldAfter[f]; !set {
				return fmt.Errorf("a revert sets %s, which its entry did not", f)
			}
			if !sameRaw(v, old[f]) {
				return fmt.Errorf("a revert sets %s to something other than what it was before the entry", f)
			}
		}
	}
	return nil
}

// translated is the before or after of an entry in the numbers of this node.
func (p *boardPlan) translated(entity string, m map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	s, err := p.logJSON(entity, m)
	if err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	return out, json.Unmarshal([]byte(s), &out)
}
