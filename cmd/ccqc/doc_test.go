package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

func TestExportWritesTheTreeAsMarkdownFiles(t *testing.T) {
	pages := map[string]string{
		"/arch":        "# Architecture\n",
		"/arch/broker": "# Broker\nstate lives in SQLite\n",
		"/notes":       "just a note\n",
	}
	call := func(a proto.DocArgs) (proto.DocReply, error) {
		switch a.Op {
		case "tree":
			var r proto.DocReply
			for _, p := range []string{"/arch", "/arch/broker", "/notes"} {
				r.Paths = append(r.Paths, p)
			}
			return r, nil
		case "get":
			if !a.Full {
				t.Errorf("export must ask for whole pages")
			}
			return proto.DocReply{Body: pages[a.Path]}, nil
		}
		t.Fatalf("unexpected op %q", a.Op)
		return proto.DocReply{}, nil
	}
	dir := t.TempDir()
	if err := exportDocs(call, dir); err != nil {
		t.Fatal(err)
	}
	for path, want := range pages {
		file := filepath.Join(dir, filepath.FromSlash(path[1:])+".md")
		got, err := os.ReadFile(file)
		if err != nil || string(got) != want {
			t.Fatalf("%s: %q, %v; want %q", file, got, err, want)
		}
	}
}
