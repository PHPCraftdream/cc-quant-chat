package mcp

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

func TestStatusLineAsChatStateShowsIt(t *testing.T) {
	now := time.Date(2026, 11, 11, 18, 0, 0, 0, time.UTC)
	since := now.Add(-12 * time.Minute).Format(time.RFC3339)
	cases := []struct {
		m    proto.MemberInfo
		want string
	}{
		{proto.MemberInfo{Activity: "busy", BusySince: since, Folder: "cc-quant-chat", Branch: "main", Note: "fixing CI"},
			"busy 12m · cc-quant-chat@main · «fixing CI»"},
		{proto.MemberInfo{Activity: "free", Folder: "x"}, "free · x"},
		{proto.MemberInfo{Folder: "x", Branch: "dev"}, "x@dev"},
		{proto.MemberInfo{}, ""}, // a client that reports nothing: just "online"
	}
	for _, c := range cases {
		if got := statusLine(c.m, now); got != c.want {
			t.Errorf("statusLine(%+v) = %q, want %q", c.m, got, c.want)
		}
	}
}

func TestSinceFormats(t *testing.T) {
	for d, want := range map[time.Duration]string{
		45 * time.Second: "45s", 12 * time.Minute: "12m", 2*time.Hour + 5*time.Minute: "2h05m",
	} {
		if got := fmtSince(d); got != want {
			t.Errorf("fmtSince(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestStateListsStatusOfOnlineMembersOnly(t *testing.T) {
	st := &proto.State{Chat: "team", Me: "dev", Members: []proto.MemberInfo{
		{Name: "dev", Online: true, Activity: "free"},
		{Name: "alice", Online: true, Role: "lead", Activity: "busy", BusySince: time.Now().Add(-5 * time.Minute).Format(time.RFC3339), Note: "tests"},
		{Name: "bob", Online: false, Note: "stale"},
	}}
	out := renderState("team", st)
	for _, want := range []string{"@dev (you)", "online · free", "online · busy 5m · «tests»"} {
		if !strings.Contains(out, want) {
			t.Errorf("chat_state lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "stale") {
		t.Errorf("an offline member's note is shown:\n%s", out)
	}
}

func TestBusyAndWaitHooksMoveTheStatus(t *testing.T) {
	t.Setenv("CCQC_HOME", t.TempDir()) // the session id starts an auto-join that reads the user config
	t.Setenv("CLAUDE_PROJECT_DIR", t.TempDir())
	s := newSession(func(string, map[string]string) {})
	hook := func(line string) net.Conn {
		srv, cl := net.Pipe()
		go s.handshake(srv)
		if _, err := fmt.Fprint(cl, line); err != nil {
			t.Fatal(err)
		}
		return cl
	}
	waitFor := func(want bool) {
		t.Helper()
		for i := 0; i < 200; i++ {
			if s.isBusy() == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("busy = %v, want %v", s.isBusy(), want)
	}

	hook("busy sess-1\n").Close() // UserPromptSubmit
	waitFor(true)
	w := hook("wait sess-1\n") // the Stop hook of the finished turn
	waitFor(false)
	w.Close()

	hook("busy sess-1\n").Close()
	waitFor(true)
	w1 := hook("wait sess-1\n")
	waitFor(false)
	hook("busy sess-1\n").Close()
	waitFor(true)
	// A second waiter is told "busy" and leaves, but it still means the turn ended.
	w2 := hook("wait sess-1\n")
	waitFor(false)
	w1.Close()
	w2.Close()
}

func TestFullStatusCarriesTheNoteAndProjectFacts(t *testing.T) {
	t.Setenv("CLAUDE_PROJECT_DIR", t.TempDir())
	s := newSession(func(string, map[string]string) {})
	cc := &chatConn{s: s, local: "team", note: "reviewing"}
	a := cc.fullStatus()
	if a.Text == nil || *a.Text != "reviewing" || a.Busy == nil || *a.Busy || a.Folder == nil || *a.Folder == "" || a.Branch == nil {
		t.Fatalf("full status = %+v", a)
	}
	s.setBusy(true)
	if b := cc.fullStatus(); b.Busy == nil || !*b.Busy {
		t.Fatalf("busy not carried after a reconnect: %+v", b)
	}
}
