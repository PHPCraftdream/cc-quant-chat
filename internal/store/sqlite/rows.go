package sqlite

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// The board and the document tree write their own rows (with their own rules). The event of such
// a change carries images of the rows it wrote, as they were when the change was done; a rebuild
// writes the images back in event order and so reaches the same rows without running the rules
// again.

// imageTables: the tables the images belong to and the columns that identify a row.
var imageTables = map[string][]string{
	"board_sprints": {"id"}, "board_tasks": {"id"}, "board_workflow": nil, "board_log": {"id"}, "board_comments": {"id"},
	"docs": {"path"}, "doc_versions": {"path", "version"},
}

type touch struct {
	table string
	key   []any
}

// recorder notes which rows a transaction of the board or of documents wrote.
type recorder struct {
	c       *chatDB
	tx      *sql.Tx
	touched []touch
	lamport int64 // the chat's clock at the last write of the transaction
}

func (r *recorder) touch(table string, key ...any) {
	for _, t := range r.touched {
		if t.table == table && fmt.Sprint(t.key...) == fmt.Sprint(key...) {
			return
		}
	}
	r.touched = append(r.touched, touch{table, key})
}

type image struct {
	T string         `json:"t"`
	R map[string]any `json:"r"`
}

// row reads one row of a table by its key as an image.
func (r *recorder) row(t touch) (image, bool, error) {
	q := `SELECT * FROM ` + t.table
	var args []any
	for i, c := range imageTables[t.table] {
		if i == 0 {
			q += ` WHERE `
		} else {
			q += ` AND `
		}
		q += c + ` = ?`
		args = append(args, t.key[i])
	}
	rows, err := r.tx.Query(q, args...)
	if err != nil {
		return image{}, false, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return image{}, false, err
	}
	if !rows.Next() {
		return image{}, false, rows.Err()
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return image{}, false, err
	}
	img := image{T: t.table, R: map[string]any{}}
	for i, c := range cols {
		if b, ok := vals[i].([]byte); ok {
			vals[i] = string(b)
		}
		img.R[c] = vals[i]
	}
	return img, true, rows.Err()
}

// RecordEvent writes the event of the transaction's change; see store.BoardTx.
func (r *recorder) RecordEvent(in store.EventIn) error {
	if r.lamport == 0 {
		return nil // nothing was written
	}
	imgs := []image{}
	for _, t := range r.touched {
		img, ok, err := r.row(t)
		if err != nil {
			return err
		}
		if ok {
			imgs = append(imgs, img)
		}
	}
	assigned, err := jsonText(map[string]any{"actor": in.Actor, "owner": in.Owner, "grant": in.Grant, "rows": imgs, "changes": changesOf(imgs)})
	if err != nil {
		return err
	}
	ts := in.TS
	if ts == "" {
		ts = now()
	}
	_, err = r.c.appendEvent(r.tx, events.Event{Lamport: r.lamport, Kind: in.Kind, Payload: in.Payload,
		AuthorPub: in.AuthorPub, Sig: in.Sig, Assigned: assigned, TS: ts})
	return err
}

// applyImages writes the row images of an event. The unique index of sprints (one active sprint)
// is kept by writing the active sprint last.
func applyImages(tx *sql.Tx, e events.Event) error {
	if e.Assigned == "" || e.Assigned == "{}" {
		return nil
	}
	var a struct {
		Rows []image `json:"rows"`
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(e.Assigned)))
	dec.UseNumber()
	if err := dec.Decode(&a); err != nil {
		return fmt.Errorf("event %d (%s): assigned: %w", e.Seq, e.Kind, err)
	}
	var late []image
	for _, img := range a.Rows {
		if img.T == "board_sprints" && img.R["state"] == "active" && img.R["deleted"] == json.Number("0") {
			late = append(late, img)
			continue
		}
		if err := writeImage(tx, img); err != nil {
			return err
		}
	}
	for _, img := range late {
		if err := writeImage(tx, img); err != nil {
			return err
		}
	}
	return nil
}

func writeImage(tx *sql.Tx, img image) error {
	if _, ok := imageTables[img.T]; !ok {
		return fmt.Errorf("an image of the unknown table %q", img.T)
	}
	cols := make([]string, 0, len(img.R))
	for c := range img.R {
		cols = append(cols, c)
	}
	sort.Strings(cols)
	var args []any
	for _, c := range cols {
		v := img.R[c]
		if n, ok := v.(json.Number); ok {
			if i, err := n.Int64(); err == nil {
				v = i
			} else if f, err := n.Float64(); err == nil {
				v = f
			}
		}
		args = append(args, v)
	}
	_, err := tx.Exec(`INSERT OR REPLACE INTO `+img.T+` (`+strings.Join(cols, ", ")+`) VALUES (?`+strings.Repeat(", ?", len(cols)-1)+`)`, args...)
	return err
}

// changesOf lists what a change did field by field, as the journal rows of the board say it
// (before and after of each field that changed). The images are the node's claim; a node that
// receives the event can check the claim against the signed request without running the rules
// again, e.g. that task.move touched only the status.
func changesOf(imgs []image) []map[string]any {
	out := []map[string]any{}
	for _, img := range imgs {
		if img.T != "board_log" {
			continue
		}
		var before, after map[string]json.RawMessage
		_ = json.Unmarshal([]byte(fmt.Sprint(img.R["before_json"])), &before)
		_ = json.Unmarshal([]byte(fmt.Sprint(img.R["after_json"])), &after)
		fields := map[string]any{}
		for f, v := range after {
			fields[f] = map[string]any{"before": before[f], "after": v}
		}
		for f, v := range before {
			if _, ok := after[f]; !ok {
				fields[f] = map[string]any{"before": v, "after": nil}
			}
		}
		out = append(out, map[string]any{"entity": img.R["entity"], "entity_id": img.R["entity_id"], "kind": img.R["kind"], "fields": fields})
	}
	return out
}
