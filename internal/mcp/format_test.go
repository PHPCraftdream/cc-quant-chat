package mcp

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// The text below is parsed by the plugin-ui cards (plugin-ui/hooks/format.ts, tested against
// the same samples in plugin-ui/hooks/format.test.ts). These tests pin what the formatters
// print to those samples: when the wire format changes, change fixtures.ts and format.ts too.

const fixturesFile = "../../plugin-ui/hooks/fixtures.ts"

var fixtureRe = regexp.MustCompile("(?s)// @fixture ([a-z-]+)\nexport const \\w+ = `(.*?)`\n// @end")

func fixtures(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(fixturesFile)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, m := range fixtureRe.FindAllStringSubmatch(strings.ReplaceAll(string(raw), "\r\n", "\n"), -1) {
		out[m[1]] = m[2]
	}
	if len(out) == 0 {
		t.Fatal("no fixtures found in " + fixturesFile)
	}
	return out
}

func sample() (a, b, c, e proto.Message) {
	a = proto.Message{ID: 5, TS: "2026-11-11T17:01:02Z", Room: "common", From: "alice", FromRole: "lead", Text: "hello all"}
	b = proto.Message{ID: 6, TS: "2026-11-11T17:01:03Z", Room: "api", From: "bob", To: "dev", ReplyTo: 5, Text: "multi\nline"}
	c = proto.Message{ID: 7, TS: "2026-11-11T17:01:04Z", Room: proto.RoomDirect, From: "carol", To: "dev", Text: "psst"}
	e = proto.Message{ID: 7, TS: "2026-11-11T17:01:05Z", Room: "api", From: "dev", Text: "ack"}
	return
}

func TestWireFormat(t *testing.T) {
	fx := fixtures(t)
	a, b, c, e := sample()

	items := []pendItem{
		{line: eventLine("team", &a, false)},
		{line: eventLine("team", &b, true)},
		{line: eventLine("team", &c, true)},
		{line: presenceLine("team", []string{"bob is back", "carol joined"})},
	}
	if got := renderBatch(items); got != fx["batch"] {
		t.Errorf("wake-up batch changed:\n%s\nwant (fixtures.ts, batch):\n%s", got, fx["batch"])
	}

	// chat_history: one fmtMsg per line, then the paging hint (see Session.history).
	history := fmtMsg("team", &a) + "\n" + fmtMsg("team", &b) + "\n" + "(older: chat_history before=5)\n"
	if history != fx["history"] {
		t.Errorf("history changed:\n%s\nwant:\n%s", history, fx["history"])
	}

	ownerMsg := proto.Message{ID: 8, TS: "2026-11-11T17:01:06Z", Room: "common", From: "owner", To: "dev", Text: "stop the deploy", Owner: true}
	if got := renderBatch([]pendItem{{line: eventLine("team", &ownerMsg, true)}}); got != fx["owner"] {
		t.Errorf("owner message changed:\n%s\nwant:\n%s", got, fx["owner"])
	}

	if got := fmtHits("team", 2, []proto.Message{a, b}); got != fx["search"] {
		t.Errorf("search changed:\n%s\nwant:\n%s", got, fx["search"])
	}

	ctx := proto.ContextResult{
		Blocks:  []proto.ContextBlock{{Thread: "api", Hits: []int64{6}, Messages: []proto.Message{b, e}}},
		Missing: []int64{99},
	}
	if got := fmtContext("team", ctx); got != fx["context"] {
		t.Errorf("context changed:\n%s\nwant:\n%s", got, fx["context"])
	}
}

func TestWireFormatOfALongBatch(t *testing.T) {
	a, _, _, _ := sample()
	items := make([]pendItem, 52)
	for i := range items {
		items[i] = pendItem{line: eventLine("team", &a, false)}
	}
	got := renderBatch(items)
	head := fixtures(t)["omitted-head"]
	if !strings.HasPrefix(got, head) {
		t.Errorf("batch of 52 starts:\n%s\nwant it to start with:\n%s", got, head)
	}
	if n := strings.Count(got, "\n[/"); n != 50 { // the last 50 events are shown
		t.Errorf("%d event lines, want 50", n)
	}
}
