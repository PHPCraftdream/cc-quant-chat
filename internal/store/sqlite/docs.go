package sqlite

import (
	"database/sql"
	"errors"

	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

var _ store.Docs = (*DB)(nil)

// DoDocs runs fn in one transaction on the document tree of a chat.
func (d *DB) DoDocs(chatID string, fn func(tx store.DocsTx) error) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if err := fn(&docsTx{tx: tx, chat: chatID}); err != nil {
		return err
	}
	return tx.Commit()
}

// ViewDocs runs fn on a snapshot; nothing it does is kept.
func (d *DB) ViewDocs(chatID string, fn func(tx store.DocsTx) error) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // reads only
	return fn(&docsTx{tx: tx, chat: chatID})
}

type docsTx struct {
	tx   *sql.Tx
	chat string
}

const docCols = `path, title, body, version, author, author_owner, ts, deleted`

func scanDoc(row interface{ Scan(...any) error }) (store.Doc, error) {
	var d store.Doc
	var owner, del int
	err := row.Scan(&d.Path, &d.Title, &d.Body, &d.Version, &d.Author, &owner, &d.TS, &del)
	d.AuthorOwner, d.Deleted = owner != 0, del != 0
	return d, err
}

func (t *docsTx) GetDoc(path string) (store.Doc, bool, error) {
	d, err := scanDoc(t.tx.QueryRow(`SELECT `+docCols+` FROM docs WHERE chat_id = ? AND path = ?`, t.chat, path))
	if errors.Is(err, sql.ErrNoRows) {
		return store.Doc{}, false, nil
	}
	return d, err == nil, err
}

func (t *docsTx) PutDoc(d store.Doc) error {
	_, err := t.tx.Exec(`INSERT OR REPLACE INTO docs (chat_id, `+docCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.chat, d.Path, d.Title, d.Body, d.Version, d.Author, boolInt(d.AuthorOwner), d.TS, boolInt(d.Deleted))
	return err
}

// under: the SQL condition "path is prefix or lies below it" (paths hold only [a-z0-9-/]).
func under(prefix string) (string, []any) {
	if prefix == "" || prefix == "/" {
		return "", nil
	}
	return ` AND (path = ? OR path LIKE ?)`, []any{prefix, prefix + "/%"}
}

func (t *docsTx) ListDocs(prefix string) ([]store.DocMeta, error) {
	cond, args := under(prefix)
	rows, err := t.tx.Query(`SELECT path, title, length(CAST(body AS BLOB)), version, author, ts FROM docs WHERE chat_id = ? AND deleted = 0`+cond+` ORDER BY path`,
		append([]any{t.chat}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.DocMeta
	for rows.Next() {
		var m store.DocMeta
		if err := rows.Scan(&m.Path, &m.Title, &m.Size, &m.Version, &m.Author, &m.TS); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (t *docsTx) ListDocBodies(prefix string) ([]store.Doc, error) {
	cond, args := under(prefix)
	rows, err := t.tx.Query(`SELECT `+docCols+` FROM docs WHERE chat_id = ? AND deleted = 0`+cond+` ORDER BY path`,
		append([]any{t.chat}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Doc
	for rows.Next() {
		d, err := scanDoc(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

const versionCols = `path, version, ts, author, author_owner, title, body, note, deleted, origin, lamport, author_pub, sig, payload`

func scanVersion(row interface{ Scan(...any) error }) (store.DocVersion, error) {
	var v store.DocVersion
	var owner, del int
	err := row.Scan(&v.Path, &v.Version, &v.TS, &v.Author, &owner, &v.Title, &v.Body, &v.Note, &del, &v.Origin, &v.Lamport, &v.AuthorPub, &v.Sig, &v.Payload)
	v.AuthorOwner, v.Deleted = owner != 0, del != 0
	return v, err
}

func (t *docsTx) AddVersion(v store.DocVersion) error {
	_, err := t.tx.Exec(`INSERT INTO doc_versions (chat_id, `+versionCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.chat, v.Path, v.Version, v.TS, v.Author, boolInt(v.AuthorOwner), v.Title, v.Body, v.Note, boolInt(v.Deleted), v.Origin, v.Lamport,
		v.AuthorPub, v.Sig, v.Payload)
	return err
}

func (t *docsTx) GetVersion(path string, version int64) (store.DocVersion, bool, error) {
	v, err := scanVersion(t.tx.QueryRow(`SELECT `+versionCols+` FROM doc_versions WHERE chat_id = ? AND path = ? AND version = ?`, t.chat, path, version))
	if errors.Is(err, sql.ErrNoRows) {
		return store.DocVersion{}, false, nil
	}
	return v, err == nil, err
}

func (t *docsTx) ListVersions(path string, before int64, limit int) ([]store.DocVersion, error) {
	q := `SELECT ` + versionCols + ` FROM doc_versions WHERE chat_id = ? AND path = ?`
	args := []any{t.chat, path}
	if before > 0 {
		q += ` AND version < ?`
		args = append(args, before)
	}
	rows, err := t.tx.Query(q+` ORDER BY version DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.DocVersion
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (t *docsTx) AllVersions() ([]store.DocVersion, error) {
	rows, err := t.tx.Query(`SELECT `+versionCols+` FROM doc_versions WHERE chat_id = ? ORDER BY path, version`, t.chat)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.DocVersion
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (t *docsTx) NextLamport() (int64, error) {
	if _, err := t.tx.Exec(`UPDATE chats SET lamport = lamport + 1 WHERE id = ?`, t.chat); err != nil {
		return 0, err
	}
	var n int64
	err := t.tx.QueryRow(`SELECT lamport FROM chats WHERE id = ?`, t.chat).Scan(&n)
	return n, err
}
