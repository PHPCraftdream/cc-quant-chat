// Package docs is the document tree of a chat: pages by path, every write a new version. The
// versions are the journal (nothing is changed or removed in place), the pages are their latest
// state. Each function runs inside one store transaction (store.Docs.DoDocs).
package docs

import (
	"fmt"
	"strings"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// Limits.
const (
	MaxTitle     = 200
	MaxBody      = 64 << 10
	MaxDepth     = 8
	WholePageMax = 16 << 10 // a larger page is not handed over whole without a section
	TreeLines    = 100
	SearchHits   = 20
	HistoryMax   = 100
	HistoryDflt  = 20
)

// Ctx is who acts, when, and on which transaction.
type Ctx struct {
	Tx     store.DocsTx
	Actor  string
	Owner  bool
	Origin string
	Sig    store.EventSig // the author's signature of the request, written on every version it creates
	Now    time.Time
}

func (c Ctx) ts() string { return c.Now.UTC().Format(time.RFC3339) }

// Result is the outcome of a modifying call.
type Result struct {
	Text    string
	Path    string
	Version int64
	Changed bool // a new version was written
}

// NormalizePath checks a page path: /seg/seg, segments [a-z0-9-]{1,32}, at most MaxDepth.
func NormalizePath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if !strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") && p != "/" {
		return "", proto.Errf("bad_path", "a page path looks like /arch/broker (got %q)", p)
	}
	segs := strings.Split(p[1:], "/")
	if len(segs) > MaxDepth {
		return "", proto.Errf("bad_path", "a path is at most %d segments deep", MaxDepth)
	}
	for _, s := range segs {
		if !proto.ValidName(s) {
			return "", proto.Errf("bad_path", "segment %q: want [a-z0-9-]{1,32}", s)
		}
	}
	return p, nil
}

// NormalizePrefix is NormalizePath that also accepts "" and "/" (the whole tree).
func NormalizePrefix(p string) (string, error) {
	if q := strings.TrimSpace(p); q == "" || q == "/" {
		return "", nil
	}
	return NormalizePath(p)
}

func depthOf(path string) int {
	if path == "" {
		return 0
	}
	return strings.Count(path, "/")
}

func fmtSize(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f KiB", float64(n)/1024)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// current returns a page's latest state and whether it is live (exists and not deleted).
func current(tx store.DocsTx, path string) (store.Doc, bool, error) {
	d, ok, err := tx.GetDoc(path)
	return d, ok && !d.Deleted, err
}

// save writes the next version of a page and updates the page, in this transaction.
func save(c Ctx, cur store.Doc, path, title, body, note string, deleted bool) (store.DocVersion, error) {
	lamport, err := c.Tx.NextLamport()
	if err != nil {
		return store.DocVersion{}, err
	}
	return saveAt(c, lamport, cur, path, title, body, note, deleted)
}

// saveAt writes a version with the given logical clock value; a move writes two versions of one
// event, which share it.
func saveAt(c Ctx, lamport int64, cur store.Doc, path, title, body, note string, deleted bool) (store.DocVersion, error) {
	v := store.DocVersion{Path: path, Version: cur.Version + 1, TS: c.ts(), Author: c.Actor, AuthorOwner: c.Owner,
		Title: title, Body: body, Note: note, Deleted: deleted, Origin: c.Origin, Lamport: lamport,
		AuthorPub: c.Sig.Pub, Sig: c.Sig.Sig, Payload: c.Sig.Payload}
	if err := c.Tx.AddVersion(v); err != nil {
		return v, err
	}
	err := c.Tx.PutDoc(store.Doc{Path: path, Title: title, Body: body, Version: v.Version, Author: c.Actor,
		AuthorOwner: c.Owner, TS: v.TS, Deleted: deleted})
	return v, err
}

func conflict(path string, cur store.Doc, base int64) error {
	return proto.Errf("conflict", "%s is at v%d (by @%s), not v%d: read it again and put the result with base=%d",
		path, cur.Version, cur.Author, base, cur.Version)
}

func validate(title, body string) error {
	if len(title) > MaxTitle {
		return proto.Errf("too_large", "title is %d bytes, max %d", len(title), MaxTitle)
	}
	if len(body) > MaxBody {
		return proto.Errf("too_large", "body is %d bytes, max %d", len(body), MaxBody)
	}
	return nil
}

// derivedTitle: the first heading of the body, else the last path segment.
func derivedTitle(path, body string) string {
	for _, h := range headings(body) {
		return h.Title
	}
	return path[strings.LastIndex(path, "/")+1:]
}

// Put creates a page or replaces its body. Replacing needs base, the version the author read.
func Put(c Ctx, path string, title *string, body string, base *int64, note string) (Result, error) {
	path, err := NormalizePath(path)
	if err != nil {
		return Result{}, err
	}
	cur, live, err := current(c.Tx, path)
	if err != nil {
		return Result{}, err
	}
	t := ""
	switch {
	case title != nil:
		t = oneLine(*title)
	case live:
		t = cur.Title
	default:
		t = derivedTitle(path, body)
	}
	if err := validate(t, body); err != nil {
		return Result{}, err
	}
	if live {
		if base == nil {
			return Result{}, proto.Errf("base_required", "%s exists at v%d (by @%s): read it, then put with base=%d", path, cur.Version, cur.Author, cur.Version)
		}
		if *base != cur.Version {
			return Result{}, conflict(path, cur, *base)
		}
		if cur.Body == body && cur.Title == t {
			return Result{Text: fmt.Sprintf("%s unchanged (v%d)", path, cur.Version), Path: path, Version: cur.Version}, nil
		}
	} else if base != nil && *base != 0 {
		return Result{}, proto.Errf("conflict", "%s does not exist (any more); put it without base to create it", path)
	}
	v, err := save(c, cur, path, t, body, note, false)
	if err != nil {
		return Result{}, err
	}
	verb := "saved"
	if !live {
		verb = "created"
	}
	return Result{Text: fmt.Sprintf("%s %s v%d (%s)", path, verb, v.Version, fmtSize(len(body))), Path: path, Version: v.Version, Changed: true}, nil
}

// Append adds text at the end of a page (for logs and notes: no need to send the whole page).
func Append(c Ctx, path, text string, base *int64) (Result, error) {
	path, err := NormalizePath(path)
	if err != nil {
		return Result{}, err
	}
	cur, live, err := current(c.Tx, path)
	if err != nil {
		return Result{}, err
	}
	if !live {
		return Result{}, proto.Errf("no_such_page", "there is no page %s; create it with put", path)
	}
	if base != nil && *base != cur.Version {
		return Result{}, conflict(path, cur, *base)
	}
	if strings.TrimSpace(text) == "" {
		return Result{}, proto.Errf("bad_args", "nothing to append")
	}
	body := cur.Body
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	body += text
	if err := validate(cur.Title, body); err != nil {
		return Result{}, err
	}
	v, err := save(c, cur, path, cur.Title, body, "append", false)
	if err != nil {
		return Result{}, err
	}
	return Result{Text: fmt.Sprintf("%s appended v%d (%s)", path, v.Version, fmtSize(len(body))), Path: path, Version: v.Version, Changed: true}, nil
}

// Move renames a page: the old path gets a deleting version, the new one a first (or next)
// version with the same content. Both are in the journal, so history shows it and revert can
// undo either half.
func Move(c Ctx, path, to string) (Result, error) {
	path, err := NormalizePath(path)
	if err != nil {
		return Result{}, err
	}
	to, err = NormalizePath(to)
	if err != nil {
		return Result{}, err
	}
	if path == to {
		return Result{}, proto.Errf("bad_args", "to is the same path")
	}
	cur, live, err := current(c.Tx, path)
	if err != nil {
		return Result{}, err
	}
	if !live {
		return Result{}, proto.Errf("no_such_page", "there is no page %s", path)
	}
	dst, dstLive, err := current(c.Tx, to)
	if err != nil {
		return Result{}, err
	}
	if dstLive {
		return Result{}, proto.Errf("exists", "%s exists already; move onto it is not allowed", to)
	}
	lamport, err := c.Tx.NextLamport()
	if err != nil {
		return Result{}, err
	}
	v, err := saveAt(c, lamport, dst, to, cur.Title, cur.Body, "moved from "+path, false)
	if err != nil {
		return Result{}, err
	}
	if _, err := saveAt(c, lamport, cur, path, cur.Title, cur.Body, "moved to "+to, true); err != nil {
		return Result{}, err
	}
	return Result{Text: fmt.Sprintf("%s → %s (%s v%d)", path, to, to, v.Version), Path: to, Version: v.Version, Changed: true}, nil
}

// Delete hides a page; its versions stay and history/revert can bring it back.
func Delete(c Ctx, path string) (Result, error) {
	path, err := NormalizePath(path)
	if err != nil {
		return Result{}, err
	}
	cur, live, err := current(c.Tx, path)
	if err != nil {
		return Result{}, err
	}
	if !live {
		return Result{}, proto.Errf("no_such_page", "there is no page %s", path)
	}
	v, err := save(c, cur, path, cur.Title, cur.Body, "deleted", true)
	if err != nil {
		return Result{}, err
	}
	return Result{Text: fmt.Sprintf("%s deleted (v%d; history keeps it)", path, v.Version), Path: path, Version: v.Version, Changed: true}, nil
}

// Revert writes a new version with the content of an old one (deleted or not). It is a version
// like any other, so it can be reverted too.
func Revert(c Ctx, path string, version int64) (Result, error) {
	path, err := NormalizePath(path)
	if err != nil {
		return Result{}, err
	}
	old, ok, err := c.Tx.GetVersion(path, version)
	if err != nil {
		return Result{}, err
	}
	if !ok {
		return Result{}, proto.Errf("no_such_version", "%s has no version v%d", path, version)
	}
	cur, _, err := c.Tx.GetDoc(path)
	if err != nil {
		return Result{}, err
	}
	if cur.Title == old.Title && cur.Body == old.Body && cur.Deleted == old.Deleted {
		return Result{Text: fmt.Sprintf("%s already has the content of v%d", path, version), Path: path, Version: cur.Version}, nil
	}
	v, err := save(c, cur, path, old.Title, old.Body, fmt.Sprintf("revert to v%d", version), old.Deleted)
	if err != nil {
		return Result{}, err
	}
	state := ""
	if old.Deleted {
		state = " (deleted, as v" + fmt.Sprint(version) + " was)"
	}
	return Result{Text: fmt.Sprintf("%s reverted to the content of v%d as v%d%s", path, version, v.Version, state), Path: path, Version: v.Version, Changed: true}, nil
}
