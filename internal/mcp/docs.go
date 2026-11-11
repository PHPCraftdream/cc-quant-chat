package mcp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// The doc tool: the chat's document tree, shared memory of the agents and the owner. One tool
// with an `op` keeps its description short; pages are markdown, every write is a new version.

type docToolArgs struct {
	Chat    string  `json:"chat"`
	Op      string  `json:"op"`
	Path    string  `json:"path"`
	To      string  `json:"to"`
	Prefix  string  `json:"prefix"`
	Depth   int     `json:"depth"`
	Title   *string `json:"title"`
	Body    *string `json:"body"`
	Text    string  `json:"text"`
	Base    *int64  `json:"base"`
	Note    string  `json:"note"`
	Section string  `json:"section"`
	Version int64   `json:"version"`
	Query   string  `json:"query"`
	Limit   int     `json:"limit"`
	Before  int64   `json:"before"`
}

var docOps = []string{"tree", "outline", "get", "put", "append", "move", "delete", "search", "history", "revert"}

func (a docToolArgs) build() (proto.DocArgs, error) {
	ok := false
	for _, o := range docOps {
		ok = ok || o == a.Op
	}
	if !ok {
		return proto.DocArgs{}, fmt.Errorf("doc: op is one of %s", strings.Join(docOps, ", "))
	}
	out := proto.DocArgs{Op: a.Op, Path: a.Path, To: a.To, Prefix: a.Prefix, Depth: a.Depth, Title: a.Title, Text: a.Text,
		Base: a.Base, Note: a.Note, Section: a.Section, Version: a.Version, Query: a.Query, Limit: a.Limit, Before: a.Before}
	if a.Body != nil {
		out.Body = *a.Body
	}
	switch a.Op {
	case "outline", "get", "append", "delete", "history", "revert", "put", "move":
		if strings.TrimSpace(a.Path) == "" {
			return out, fmt.Errorf("doc %s: path is required (like /arch/broker)", a.Op)
		}
	}
	switch {
	case a.Op == "put" && a.Body == nil:
		return out, fmt.Errorf("doc put: body is required")
	case a.Op == "move" && a.To == "":
		return out, fmt.Errorf("doc move: to is required")
	case a.Op == "revert" && a.Version <= 0:
		return out, fmt.Errorf("doc revert: version is required (see doc history)")
	case a.Op == "search" && strings.TrimSpace(a.Query) == "":
		return out, fmt.Errorf("doc search: query is required")
	}
	return out, nil
}

// docTool runs one call of the doc tool.
func (s *Session) docTool(raw json.RawMessage) (string, error) {
	var a docToolArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("bad arguments: %v", err)
	}
	args, err := a.build()
	if err != nil {
		return "", err
	}
	cc, err := s.get(a.Chat)
	if err != nil {
		return "", err
	}
	var r proto.DocReply
	call := func() error { return cc.call("doc", &args, &r) }
	if proto.DocWrite(args.Op) {
		call = func() error { return cc.write("doc", "doc."+args.Op, &args, &r) }
	}
	if err := call(); err != nil {
		if proto.IsCode(err, "unknown_op") || proto.IsCode(err, "not_joined") {
			return "", fmt.Errorf("the running broker is older and has no document tree: run `ccqc broker restart`")
		}
		return "", err
	}
	return strings.TrimRight(r.Text, "\n"), nil
}
