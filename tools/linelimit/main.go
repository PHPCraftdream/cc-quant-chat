// linelimit fails when a source file is longer than the limit; such files must be split.
//
//	go run ./tools/linelimit [-max 500] [dir ...]
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var codeExt = map[string]bool{
	".go": true, ".rs": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true,
	".py": true, ".sh": true, ".ps1": true, ".c": true, ".h": true, ".cpp": true,
}

var skipDir = map[string]bool{".git": true, "vendor": true, "node_modules": true, "bin": true, "target": true}

type violation struct {
	path  string
	lines int
}

func main() {
	max := flag.Int("max", 500, "maximum lines per source file")
	flag.Parse()
	roots := flag.Args()
	if len(roots) == 0 {
		roots = []string{"."}
	}
	var bad []violation
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if p != root && (skipDir[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
					return filepath.SkipDir
				}
				return nil
			}
			if !codeExt[strings.ToLower(filepath.Ext(p))] {
				return nil
			}
			n, err := countLines(p)
			if err != nil {
				return err
			}
			if n > *max {
				bad = append(bad, violation{filepath.ToSlash(p), n})
			}
			return nil
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "linelimit:", err)
			os.Exit(2)
		}
	}
	if len(bad) == 0 {
		return
	}
	sort.Slice(bad, func(i, j int) bool { return bad[i].lines > bad[j].lines })
	for _, v := range bad {
		fmt.Printf("%s: %d lines (max %d) — split it into smaller files\n", v.path, v.lines, *max)
	}
	os.Exit(1)
}

func countLines(p string) (int, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return 0, err
	}
	if len(b) == 0 {
		return 0, nil
	}
	n := bytes.Count(b, []byte{'\n'})
	if b[len(b)-1] != '\n' {
		n++
	}
	return n, nil
}
