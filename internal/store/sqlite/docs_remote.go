package sqlite

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/events"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// The documents of a chat when other nodes write to it (docs/design-federation.md §4).
//
// A version names the version it was written over (its parent), by the global name of the event that
// made that version: (origin, seq) of the event and the path (a move makes two versions in one event).
// A page's heads are the versions nothing has been written over. One head is the page; two heads
// are two writes over the same version: a conflict. Both versions are kept, the page shows the later
// one by (lamport, origin), and the next write, which a node makes over all the heads it knows,
// resolves it. The numbers vN are each node's own, in the order it applied the versions; the page's
// version is the highest of them, so that a node's next number never meets one that is taken.

type docAssigned struct {
	Rows     []image               `json:"rows"`
	DParents map[string][]eventRef `json:"dparents,omitempty"`
}

func parseDocs(assigned string) (docAssigned, error) {
	var a docAssigned
	if assigned == "" || assigned == "{}" {
		return a, nil
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(assigned)))
	dec.UseNumber()
	return a, dec.Decode(&a)
}

// The limits of the document tree (package docs, which this package cannot import).
const (
	maxPageBody  = 64 << 10
	maxPageTitle = 200
	maxPageDepth = 8
)

// validPagePath: /seg/seg, segments [a-z0-9-]{1,32}, at most maxPageDepth of them.
func validPagePath(p string) bool {
	if !strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") {
		return false
	}
	segs := strings.Split(p[1:], "/")
	if len(segs) > maxPageDepth {
		return false
	}
	for _, s := range segs {
		if !proto.ValidName(s) {
			return false
		}
	}
	return true
}

func isDocKind(kind string) bool {
	switch kind {
	case "doc.put", "doc.append", "doc.move", "doc.delete", "doc.revert":
		return true
	}
	return false
}

// docParents names, for each page the images write a version of, the versions that are its heads
// now (before the event): what the new version is written over.
func docParents(tx *sql.Tx, imgs []image) (map[string][]eventRef, error) {
	out := map[string][]eventRef{}
	for _, img := range imgs {
		if img.T != "doc_versions" {
			continue
		}
		path := asStr(img.R["path"])
		if _, done := out[path]; done {
			continue
		}
		rows, err := tx.Query(`SELECT v.origin, v.seq FROM doc_heads h JOIN doc_vrefs v ON v.path = h.path AND v.version = h.version
			WHERE h.path = ? ORDER BY v.origin, v.seq`, path)
		if err != nil {
			return nil, err
		}
		var refs []eventRef
		for rows.Next() {
			var r eventRef
			if err := rows.Scan(&r.Origin, &r.Seq); err != nil {
				rows.Close()
				return nil, err
			}
			refs = append(refs, r)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if len(refs) > 0 {
			out[path] = refs
		}
	}
	return out, nil
}

// noteDocs records, for an event of this node, the names of the versions it made and what they
// replaced as heads.
func noteDocs(tx *sql.Tx, e events.Event, imgs []image, parents map[string][]eventRef) error {
	for _, img := range imgs {
		if img.T != "doc_versions" {
			continue
		}
		path, ver := asStr(img.R["path"]), asInt(img.R["version"])
		if _, err := tx.Exec(`INSERT OR REPLACE INTO doc_vrefs (path, version, origin, seq) VALUES (?, ?, ?, ?)`, path, ver, e.Origin, e.Seq); err != nil {
			return err
		}
		if err := replaceHeads(tx, path, ver, parents[path]); err != nil {
			return err
		}
	}
	return nil
}

// replaceHeads makes version ver a head of the page in place of the versions it was written over.
func replaceHeads(tx *sql.Tx, path string, ver int64, parents []eventRef) error {
	for _, r := range parents {
		if _, err := tx.Exec(`DELETE FROM doc_heads WHERE path = ? AND version = (SELECT version FROM doc_vrefs WHERE path = ? AND origin = ? AND seq = ?)`,
			path, path, r.Origin, r.Seq); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`INSERT OR REPLACE INTO doc_heads (path, version) VALUES (?, ?)`, path, ver)
	return err
}

// deriveDocRefs names the versions of a chat written before the names existed, in the order they
// were applied; each was written over what was the page then.
func deriveDocRefs(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT origin, seq, lamport, assigned FROM events WHERE remote = 0 AND hidden = '' AND kind LIKE 'doc.%' ORDER BY ord`)
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
		a, err := parseDocs(e.Assigned)
		if err != nil {
			return fmt.Errorf("event %s#%d: assigned: %w", e.Origin, e.Seq, err)
		}
		parents, err := docParents(tx, a.Rows)
		if err != nil {
			return err
		}
		if err := noteDocs(tx, e, a.Rows, parents); err != nil {
			return err
		}
	}
	return nil
}

// ---- the document events of other nodes ----

func remoteDoc(tx *sql.Tx, e events.Event, self string) error {
	a, err := parseDocs(e.Assigned)
	if err != nil {
		return fmt.Errorf("%s: assigned: %w", e.Kind, err)
	}
	var versions []image
	for _, img := range a.Rows {
		switch img.T {
		case "doc_versions":
			versions = append(versions, img)
		case "docs":
		default:
			return fmt.Errorf("%s may not write the table %s", e.Kind, img.T)
		}
	}
	want := 1
	if e.Kind == "doc.move" {
		want = 2
	}
	if len(versions) != want {
		return fmt.Errorf("%s makes %d version(s), the event has %d", e.Kind, want, len(versions))
	}
	var payload map[string]any
	_ = json.Unmarshal([]byte(e.Payload), &payload)
	seen := map[string]bool{}
	for _, v := range versions {
		path := asStr(v.R["path"])
		if !validPagePath(path) || seen[path] {
			return fmt.Errorf("%s: the path %q is not a page path", e.Kind, path)
		}
		seen[path] = true
		if len(asStr(v.R["body"])) > maxPageBody || len(asStr(v.R["title"])) > maxPageTitle {
			return fmt.Errorf("%s: a version is too large", e.Kind)
		}
		if e.Kind == "doc.delete" && asInt(v.R["deleted"]) != 1 {
			return fmt.Errorf("doc.delete makes a version that is not a deletion")
		}
		if e.Kind == "doc.put" && payload["path"] == path {
			if body, ok := payload["body"].(string); ok && body != asStr(v.R["body"]) {
				return errors.New("the request says one body, the version holds another")
			}
		}
	}
	// What each version was written over, in the numbers of this node; before anything is written.
	over := map[string][]int64{}
	for path, refs := range a.DParents {
		if !seen[path] {
			return fmt.Errorf("%s: parents of a page it does not write (%s)", e.Kind, path)
		}
		for _, r := range refs {
			var ver int64
			err := tx.QueryRow(`SELECT version FROM doc_vrefs WHERE path = ? AND origin = ? AND seq = ?`, path, r.Origin, r.Seq).Scan(&ver)
			if errors.Is(err, sql.ErrNoRows) {
				var held int
				if err := tx.QueryRow(`SELECT COUNT(*) FROM events WHERE origin = ? AND seq = ?`, r.Origin, r.Seq).Scan(&held); err != nil {
					return err
				}
				if held == 0 {
					return fmt.Errorf("%w: a version of %s by %s#%d", errMissingRef, path, r.Origin, r.Seq)
				}
				return nil // written over something that can never be here: kept in the journal, not applied
			}
			if err != nil {
				return err
			}
			over[path] = append(over[path], ver)
		}
	}
	for _, v := range versions {
		path := asStr(v.R["path"])
		var n int64
		if err := tx.QueryRow(`SELECT COALESCE(MAX(version), 0) + 1 FROM doc_versions WHERE path = ?`, path).Scan(&n); err != nil {
			return err
		}
		signed := ""
		if e.Sig != "" {
			signed = e.Payload
		}
		author := asStr(v.R["author"])
		if author != "" {
			author = remoteName(self, e.Origin, author)
		}
		if _, err := tx.Exec(`INSERT INTO doc_versions (path, version, ts, author, author_owner, title, body, note, deleted, origin, lamport, author_pub, sig, payload)
			VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			path, n, asStr(v.R["ts"]), author, asStr(v.R["title"]), asStr(v.R["body"]), asStr(v.R["note"]), asInt(v.R["deleted"]),
			e.Origin, e.Lamport, e.AuthorPub, e.Sig, signed); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO doc_vrefs (path, version, origin, seq) VALUES (?, ?, ?, ?)`, path, n, e.Origin, e.Seq); err != nil {
			return err
		}
		for _, p := range over[path] {
			if _, err := tx.Exec(`DELETE FROM doc_heads WHERE path = ? AND version = ?`, path, p); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`INSERT INTO doc_heads (path, version) VALUES (?, ?)`, path, n); err != nil {
			return err
		}
		if err := recomputeDoc(tx, path); err != nil {
			return err
		}
	}
	return nil
}

// recomputeDoc sets the page from its heads: the content of the latest head by (lamport, origin),
// under the highest version number.
func recomputeDoc(tx *sql.Tx, path string) error {
	var win, top int64
	err := tx.QueryRow(`SELECT v.version FROM doc_heads h JOIN doc_versions v ON v.path = h.path AND v.version = h.version
		WHERE h.path = ? ORDER BY v.lamport DESC, v.origin DESC, v.version DESC LIMIT 1`, path).Scan(&win)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := tx.QueryRow(`SELECT MAX(version) FROM doc_versions WHERE path = ?`, path).Scan(&top); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT OR REPLACE INTO docs (path, title, body, version, author, author_owner, ts, deleted)
		SELECT path, title, body, ?, author, author_owner, ts, deleted FROM doc_versions WHERE path = ? AND version = ?`, top, path, win)
	return err
}
