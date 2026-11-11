package docs

import (
	"fmt"
	"sort"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

func shortTS(ts string) string {
	if len(ts) >= 16 {
		return strings.Replace(ts[5:16], "T", " ", 1)
	}
	return ts
}

// Tree lists the pages at or under prefix: path, title and size only, no bodies, at most
// TreeLines lines. depth (0 = unlimited) folds deeper pages into one line per folder. The paths
// of all pages come back too, for callers that walk the whole tree (export).
func Tree(tx store.DocsTx, prefix string, depth int) (string, []string, error) {
	prefix, err := NormalizePrefix(prefix)
	if err != nil {
		return "", nil, err
	}
	metas, err := tx.ListDocs(prefix)
	if err != nil {
		return "", nil, err
	}
	paths := make([]string, len(metas))
	pages := map[string]bool{}
	for i, m := range metas {
		paths[i] = m.Path
		pages[m.Path] = true
	}
	type line struct{ path, text string }
	var lines []line
	folders := map[string]int{}
	base := depthOf(prefix)
	for _, m := range metas {
		if depth > 0 && depthOf(m.Path)-base > depth {
			segs := strings.Split(m.Path[1:], "/")
			folder := "/" + strings.Join(segs[:base+depth], "/")
			if !pages[folder] { // a folder that is a page itself already has its line
				folders[folder]++
			}
			continue
		}
		lines = append(lines, line{m.Path, fmt.Sprintf("%s  %s  (%s, v%d)", m.Path, m.Title, fmtSize(m.Size), m.Version)})
	}
	for f, n := range folders {
		if n > 0 {
			lines = append(lines, line{f + "/", fmt.Sprintf("%s/  (%d more page(s) below)", f, n)})
		}
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].path < lines[j].path })
	if len(lines) == 0 {
		return "no pages here\n", paths, nil
	}
	var b strings.Builder
	for i, l := range lines {
		if i == TreeLines {
			fmt.Fprintf(&b, "…and %d more (narrow with prefix)\n", len(lines)-TreeLines)
			break
		}
		b.WriteString(l.text + "\n")
	}
	return b.String(), paths, nil
}

// Outline lists the headings of a page with the sizes of their sections.
func Outline(tx store.DocsTx, path string) (string, error) {
	path, err := NormalizePath(path)
	if err != nil {
		return "", err
	}
	d, live, err := current(tx, path)
	if err != nil {
		return "", err
	}
	if !live {
		return "", proto.Errf("no_such_page", "there is no page %s", path)
	}
	return fmt.Sprintf("%s  %s  (%s, v%d)\n%s", path, d.Title, fmtSize(len(d.Body)), d.Version, outlineText(d.Body)), nil
}

// Get returns a page, a section of it or an old version. A page larger than WholePageMax is not
// handed over whole: the outline comes back with a hint, and a section is asked for by heading.
// The second result is the page text alone (no framing), for tools that save it.
func Get(tx store.DocsTx, path, sect string, version int64, full bool) (string, string, int64, error) {
	path, err := NormalizePath(path)
	if err != nil {
		return "", "", 0, err
	}
	var title, body, who, ts string
	var v int64
	if version > 0 {
		old, ok, err := tx.GetVersion(path, version)
		if err != nil {
			return "", "", 0, err
		}
		if !ok {
			return "", "", 0, proto.Errf("no_such_version", "%s has no version v%d", path, version)
		}
		title, body, who, ts, v = old.Title, old.Body, old.Author, old.TS, old.Version
		if old.Deleted {
			title += " (deleted version)"
		}
	} else {
		d, live, err := current(tx, path)
		if err != nil {
			return "", "", 0, err
		}
		if !live {
			return "", "", 0, proto.Errf("no_such_page", "there is no page %s", path)
		}
		title, body, who, ts, v = d.Title, d.Body, d.Author, d.TS, d.Version
	}
	head := fmt.Sprintf("%s  %s  (v%d by @%s %s, %s)\n", path, title, v, who, shortTS(ts), fmtSize(len(body)))
	if sect != "" {
		s, err := section(body, sect)
		if err != nil {
			return "", "", 0, err
		}
		return head + s, s, v, nil
	}
	if len(body) > WholePageMax && !full {
		return head + fmt.Sprintf("this page is %s, too large to hand over whole (limit %s). Its outline:\n%s"+
			"Get one part with section=<heading>.\n", fmtSize(len(body)), fmtSize(WholePageMax), outlineText(body)), "", v, nil
	}
	return head + body, body, v, nil
}

// Search finds pages whose title or body contains the query (case-insensitive), at most
// SearchHits: path, title and the matching line.
func Search(tx store.DocsTx, query, prefix string) (string, error) {
	prefix, err := NormalizePrefix(prefix)
	if err != nil {
		return "", err
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return "", proto.Errf("bad_args", "query is required")
	}
	pages, err := tx.ListDocBodies(prefix)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	hits := 0
	for _, d := range pages {
		match := ""
		if strings.Contains(strings.ToLower(d.Title), q) {
			match = "(title)"
		}
		for _, l := range strings.Split(d.Body, "\n") {
			if match == "" || match == "(title)" {
				if strings.Contains(strings.ToLower(l), q) {
					match = clipLine(l, 120)
					break
				}
			}
		}
		if match == "" {
			continue
		}
		hits++
		if hits <= SearchHits {
			fmt.Fprintf(&b, "%s  %s: %s\n", d.Path, d.Title, match)
		}
	}
	switch {
	case hits == 0:
		return "no matches\n", nil
	case hits > SearchHits:
		fmt.Fprintf(&b, "…and %d more (narrow with prefix)\n", hits-SearchHits)
	}
	return b.String(), nil
}

func clipLine(s string, n int) string {
	s = oneLine(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// History lists the versions of a page, newest first, one line each.
func History(tx store.DocsTx, path string, limit int, before int64) (string, error) {
	path, err := NormalizePath(path)
	if err != nil {
		return "", err
	}
	if limit <= 0 {
		limit = HistoryDflt
	}
	if limit > HistoryMax {
		limit = HistoryMax
	}
	list, err := tx.ListVersions(path, before, limit)
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "no versions here\n", nil
	}
	var b strings.Builder
	for _, v := range list {
		who := "@" + v.Author
		if v.AuthorOwner {
			who = "OWNER " + who
		}
		note := ""
		if v.Note != "" {
			note = "  " + clipLine(v.Note, 60)
		}
		del := ""
		if v.Deleted {
			del = "  [deleted]"
		}
		fmt.Fprintf(&b, "v%d %s %s (%s)%s%s\n", v.Version, shortTS(v.TS), who, fmtSize(len(v.Body)), note, del)
	}
	if len(list) == limit {
		fmt.Fprintf(&b, "(older: before=%d)\n", list[len(list)-1].Version)
	}
	return b.String(), nil
}

// Rebuild replays every version into the pages they leave (the journal is the truth, the pages a
// cache); a test keeps the two equal.
func Rebuild(versions []store.DocVersion) map[string]store.Doc {
	out := map[string]store.Doc{}
	for _, v := range versions { // by path and version
		out[v.Path] = store.Doc{Path: v.Path, Title: v.Title, Body: v.Body, Version: v.Version, Author: v.Author,
			AuthorOwner: v.AuthorOwner, TS: v.TS, Deleted: v.Deleted}
	}
	return out
}
