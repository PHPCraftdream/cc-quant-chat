package broker

import (
	"strings"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

func ptr[T any](v T) *T { return &v }

// memberInfo: what alice sees of nick in chat_state.
func memberInfo(t *testing.T, c *client, nick string) proto.MemberInfo {
	t.Helper()
	var st proto.State
	if code := c.call("state", nil, &st); code != "" {
		t.Fatalf("state: %q", code)
	}
	for _, m := range st.Members {
		if m.Name == nick {
			return m
		}
	}
	t.Fatalf("@%s is not in the state", nick)
	return proto.MemberInfo{}
}

func TestStatusUpdatesAreFieldByField(t *testing.T) {
	b := newTestBroker(newMemStore())
	tok, _ := token(t)
	a, c := b.connect(t), b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	c.join(tok, "bob", proto.ProtoVersion)

	if code := c.call("status", proto.StatusArgs{Folder: ptr("cc-quant-chat"), Branch: ptr("main"), Text: ptr("fixing CI")}, nil); code != "" {
		t.Fatalf("status: %q", code)
	}
	if code := c.call("status", proto.StatusArgs{Busy: ptr(true)}, nil); code != "" {
		t.Fatalf("busy: %q", code)
	}
	m := memberInfo(t, a, "bob")
	if m.Folder != "cc-quant-chat" || m.Branch != "main" || m.Note != "fixing CI" || m.Activity != "busy" || m.BusySince == "" {
		t.Fatalf("a busy update changed other fields: %+v", m)
	}

	since := m.BusySince
	c.call("status", proto.StatusArgs{Busy: ptr(true)}, nil) // again: the same turn
	if got := memberInfo(t, a, "bob").BusySince; got != since {
		t.Fatalf("busy since moved from %s to %s while still busy", since, got)
	}

	c.call("status", proto.StatusArgs{Busy: ptr(false), Text: ptr("")}, nil)
	m = memberInfo(t, a, "bob")
	if m.Activity != "free" || m.BusySince != "" || m.Note != "" || m.Folder != "cc-quant-chat" {
		t.Fatalf("free and cleared note: %+v", m)
	}
}

func TestStatusIsInvisibleBeforeReportedAndAfterLeaving(t *testing.T) {
	b := newTestBroker(newMemStore())
	tok, _ := token(t)
	a, c := b.connect(t), b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	c.join(tok, "bob", proto.ProtoVersion)
	if m := memberInfo(t, a, "bob"); m.Activity != "" || m.Note != "" {
		t.Fatalf("nothing reported yet, but %+v", m)
	}
	c.call("status", proto.StatusArgs{Busy: ptr(true), Text: ptr("working")}, nil)
	if c.call("leave", nil, nil) != "" {
		t.Fatal("leave failed")
	}
	if m := memberInfo(t, a, "bob"); m.Online || m.Activity != "" || m.Note != "" {
		t.Fatalf("a member that left still shows a status: %+v", m)
	}
	c2 := b.connect(t)
	c2.key = c.key
	c2.join(tok, "bob", proto.ProtoVersion)
	if m := memberInfo(t, a, "bob"); m.Note != "" || m.Activity != "" {
		t.Fatalf("a new presence starts without the old status: %+v", m)
	}
}

func TestStatusTextIsOneShortLine(t *testing.T) {
	b := newTestBroker(newMemStore())
	tok, _ := token(t)
	a := b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	if code := a.call("status", proto.StatusArgs{Text: ptr(strings.Repeat("x", proto.MaxStatusText+1))}, nil); code != "too_large" {
		t.Fatalf("long text: %q", code)
	}
	a.call("status", proto.StatusArgs{Text: ptr("line one\nline\ttwo \x1b[31mred")}, nil)
	got := memberInfo(t, a, "alice").Note
	if strings.ContainsAny(got, "\n\t\x1b") || got != "line one line two [31mred" {
		t.Fatalf("note = %q, want one printable line", got)
	}
	a.call("status", proto.StatusArgs{Folder: ptr(strings.Repeat("f", 500)), Branch: ptr(strings.Repeat("é", 200))}, nil)
	m := memberInfo(t, a, "alice")
	if len(m.Folder) != proto.MaxFolder || len(m.Branch) > proto.MaxBranch || !strings.HasPrefix(m.Branch, "é") {
		t.Fatalf("folder %d bytes, branch %d bytes: cut to the limits on a character boundary", len(m.Folder), len(m.Branch))
	}
}

func TestStatusUpdatesAreRateLimitedPerNick(t *testing.T) {
	b := newTestBroker(newMemStore())
	tok, _ := token(t)
	a := b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	for i := 0; i < statusLimit; i++ {
		if code := a.call("status", proto.StatusArgs{Busy: ptr(i%2 == 0)}, nil); code != "" {
			t.Fatalf("update %d: %q", i, code)
		}
	}
	if code := a.call("status", proto.StatusArgs{Busy: ptr(true)}, nil); code != "rate_limited" {
		t.Fatalf("over the limit: %q", code)
	}
}

func TestStatusRaisesNoEvent(t *testing.T) {
	b := newTestBroker(newMemStore())
	tok, _ := token(t)
	a, c := b.connect(t), b.connect(t)
	a.join(tok, "alice", proto.ProtoVersion)
	c.join(tok, "bob", proto.ProtoVersion)
	a.msgs() // drain presence
	c.call("status", proto.StatusArgs{Busy: ptr(true), Text: ptr("hi")}, nil)
	if got := a.msgs(); len(got) != 0 {
		t.Fatalf("a status update produced message events: %v", got)
	}
	select {
	case ev := <-a.events:
		t.Fatalf("a status update woke alice: %+v", ev)
	default:
	}
}
