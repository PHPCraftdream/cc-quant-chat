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

// imageTables: the tables the images belong to and the columns that identify a row in a chat.
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
	d       *DB
	tx      *sql.Tx
	chat    string
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

// row reads one row of a table by its key as an image (chat_id left out: the event has it).
func (r *recorder) row(t touch) (image, bool, error) {
	q := `SELECT * FROM ` + t.table + ` WHERE chat_id = ?`
	args := []any{r.chat}
	for i, c := range imageTables[t.table] {
		q += ` AND ` + c + ` = ?`
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
		if c == "chat_id" {
			continue
		}
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
	assigned, err := jsonText(map[string]any{"actor": in.Actor, "owner": in.Owner, "grant": in.Grant, "rows": imgs})
	if err != nil {
		return err
	}
	ts := in.TS
	if ts == "" {
		ts = now()
	}
	_, err = r.d.appendEvent(r.tx, events.Event{ChatID: r.chat, Lamport: r.lamport, Kind: in.Kind, Payload: in.Payload,
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
		if err := writeImage(tx, e.ChatID, img); err != nil {
			return err
		}
	}
	for _, img := range late {
		if err := writeImage(tx, e.ChatID, img); err != nil {
			return err
		}
	}
	return nil
}

func writeImage(tx *sql.Tx, chatID string, img image) error {
	if _, ok := imageTables[img.T]; !ok {
		return fmt.Errorf("an image of the unknown table %q", img.T)
	}
	cols := make([]string, 0, len(img.R))
	for c := range img.R {
		cols = append(cols, c)
	}
	sort.Strings(cols)
	args := []any{chatID}
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
	_, err := tx.Exec(`INSERT OR REPLACE INTO `+img.T+` (chat_id, `+strings.Join(cols, ", ")+`) VALUES (?`+strings.Repeat(", ?", len(cols))+`)`, args...)
	return err
}
