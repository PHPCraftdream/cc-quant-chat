package docs

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// Heading is a markdown heading and the section it opens: from its line to the next heading of
// the same or a higher level (so a section holds its subsections).
type Heading struct {
	Level      int
	Title      string
	Start, End int // byte offsets of the section in the body
}

var headingRe = regexp.MustCompile(`^(#{1,6})[ \t]+(.*?)[ \t#]*$`)

// headings lists the headings of a markdown body, not counting lines inside fenced code.
func headings(body string) []Heading {
	var out []Heading
	inFence := false
	off := 0
	for _, line := range strings.SplitAfter(body, "\n") {
		trimmed := strings.TrimRight(line, "\r\n")
		if t := strings.TrimLeft(trimmed, " "); strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			inFence = !inFence
		} else if !inFence {
			if m := headingRe.FindStringSubmatch(trimmed); m != nil && strings.TrimSpace(m[2]) != "" {
				out = append(out, Heading{Level: len(m[1]), Title: strings.TrimSpace(m[2]), Start: off})
			}
		}
		off += len(line)
	}
	for i := range out {
		out[i].End = len(body)
		for j := i + 1; j < len(out); j++ {
			if out[j].Level <= out[i].Level {
				out[i].End = out[j].Start
				break
			}
		}
	}
	return out
}

// outlineText renders the headings with the size of their sections.
func outlineText(body string) string {
	hs := headings(body)
	if len(hs) == 0 {
		return "(no headings)\n"
	}
	var b strings.Builder
	for _, h := range hs {
		fmt.Fprintf(&b, "%s%s %s  (%s)\n", strings.Repeat("  ", h.Level-1), strings.Repeat("#", h.Level), h.Title, fmtSize(h.End-h.Start))
	}
	return b.String()
}

// section returns the part of a body under the heading named title (case-insensitive; a leading
// "##" in the name is ignored).
func section(body, title string) (string, error) {
	want := strings.ToLower(strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(title), "#")))
	hs := headings(body)
	for _, h := range hs {
		if strings.ToLower(h.Title) == want {
			return body[h.Start:h.End], nil
		}
	}
	names := make([]string, len(hs))
	for i, h := range hs {
		names[i] = h.Title
	}
	return "", proto.Errf("no_such_section", "no heading %q; headings: %s", title, strings.Join(names, " | "))
}
