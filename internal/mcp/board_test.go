package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

func build(t *testing.T, tool, js string) (proto.BoardArgs, error) {
	t.Helper()
	var a boardToolArgs
	if err := json.Unmarshal([]byte(js), &a); err != nil {
		t.Fatalf("%s: %v", js, err)
	}
	return a.build(tool)
}

func TestTaskToolBuildsTheBrokerRequest(t *testing.T) {
	a, err := build(t, "task", `{"op":"create","title":"Fix CI","assignee":"bob","priority":2,"sprint":"S-2"}`)
	if err != nil || a.Op != "task.create" || *a.Title != "Fix CI" || *a.Assignee != "bob" || *a.Priority != 2 || a.Sprint == nil || *a.Sprint != 2 {
		t.Fatalf("create: %+v, %v", a, err)
	}
	// Ids come as "T-12", "12", 12 or "#12"; the backlog as 0, "backlog" or "none".
	for _, id := range []string{`"T-12"`, `"12"`, `12`, `"#12"`, `"t12"`} {
		a, err := build(t, "task", `{"op":"move","id":`+id+`,"status":"doing"}`)
		if err != nil || a.ID != 12 || a.Status != "doing" || a.Op != "task.move" {
			t.Fatalf("id %s: %+v, %v", id, a, err)
		}
	}
	a, err = build(t, "task", `{"op":"update","id":"T-3","sprint":"backlog","title":"x"}`)
	if err != nil || a.Sprint == nil || *a.Sprint != 0 || a.Assignee != nil || a.Priority != nil {
		t.Fatalf("update leaves unnamed fields alone: %+v, %v", a, err)
	}
	a, err = build(t, "task", `{"op":"update","id":3,"assignee":""}`)
	if err != nil || a.Assignee == nil || *a.Assignee != "" {
		t.Fatalf("an empty assignee unassigns: %+v, %v", a, err)
	}
	a, err = build(t, "task", `{"op":"list","sprint":"all","status":"done","assignee":"@bob","mine":true,"done":true}`)
	if err != nil || a.SprintFilter != "all" || a.Status != "done" || a.AssigneeFilter != "@bob" || !a.Mine || !a.Done {
		t.Fatalf("list: %+v, %v", a, err)
	}
	if _, err := build(t, "task", `{"op":"move","status":"doing"}`); err == nil || !strings.Contains(err.Error(), "id is required") {
		t.Fatalf("move without an id: %v", err)
	}
	if _, err := build(t, "task", `{"op":"frobnicate"}`); err == nil || !strings.Contains(err.Error(), "create, update, move, delete, comment, comments, get, list") {
		t.Fatalf("unknown op: %v", err)
	}
	if _, err := build(t, "task", `{"op":"get","id":"banana"}`); err == nil {
		t.Fatal("a nonsense id was accepted")
	}
}

func TestSprintAndBoardToolsBuildTheirRequests(t *testing.T) {
	a, err := build(t, "sprint", `{"op":"close","id":"S-3","carry":"S-4"}`)
	if err != nil || a.Op != "sprint.close" || a.ID != 3 || a.Carry != 4 {
		t.Fatalf("close: %+v, %v", a, err)
	}
	a, err = build(t, "sprint", `{"op":"close","id":3,"carry":"backlog"}`)
	if err != nil || a.Carry != 0 {
		t.Fatalf("close to the backlog: %+v, %v", a, err)
	}
	if _, err := build(t, "sprint", `{"op":"start"}`); err == nil || !strings.Contains(err.Error(), "S-3") {
		t.Fatalf("start without an id: %v", err)
	}
	a, err = build(t, "sprint", `{"op":"create","name":"one","goal":"ship"}`)
	if err != nil || *a.Name != "one" || *a.Goal != "ship" {
		t.Fatalf("create: %+v, %v", a, err)
	}

	a, err = build(t, "board", `{"op":"log","id":"T-5","limit":10,"before":40}`)
	if err != nil || a.Op != "board.log" || a.Entity != "task" || a.ID != 5 || a.Limit != 10 || a.Before != 40 {
		t.Fatalf("log of a task: %+v, %v", a, err)
	}
	a, err = build(t, "board", `{"op":"log"}`)
	if err != nil || a.Entity != "" {
		t.Fatalf("log of the board: %+v, %v", a, err)
	}
	a, err = build(t, "board", `{"op":"revert","log_id":"#57","force":true}`)
	if err != nil || a.LogID != 57 || !a.Force || a.Op != "board.revert" {
		t.Fatalf("revert: %+v, %v", a, err)
	}
	if _, err := build(t, "board", `{"op":"revert"}`); err == nil {
		t.Fatal("revert without a log_id")
	}
	if _, err := build(t, "board", `{"op":"workflow_set"}`); err == nil {
		t.Fatal("workflow_set without a workflow")
	}
	a, err = build(t, "board", `{"op":"workflow_set","workflow":{"statuses":["a"],"initial":"a"}}`)
	if err != nil || !strings.Contains(string(a.Workflow), `"initial"`) {
		t.Fatalf("workflow_set: %+v, %v", a, err)
	}
	if _, err := build(t, "task", `{"op":"revert"}`); err == nil {
		t.Fatal("an op of another tool was accepted")
	}
}

func TestToolDefinitionsAreValidAndListTheBoardTools(t *testing.T) {
	var defs []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(toolDefs, &defs); err != nil {
		t.Fatal(err)
	}
	have := map[string]int{}
	for _, d := range defs {
		have[d.Name] = len(d.Description)
	}
	for _, name := range []string{"task", "sprint", "board", "status_set"} {
		if have[name] == 0 {
			t.Errorf("tool %s is missing or has no description", name)
		}
	}
	for _, name := range []string{"task", "sprint", "board"} {
		if have[name] > 420 { // the descriptions ride in every session's context
			t.Errorf("tool %s: description of %d bytes is not short", name, have[name])
		}
	}
}

func TestBoardEventsWakeOnlyWhenTheBrokerSaysSo(t *testing.T) {
	var woke []string
	s := newSession(func(content string, meta map[string]string) { woke = append(woke, meta["kind"]+": "+content) })
	cc := &chatConn{s: s, local: "team"}
	cc.onEvent(proto.Event{Kind: "board", Text: "@alice assigned you T-1 \"x\" (log #1)", Wake: true})
	cc.onEvent(proto.Event{Kind: "board", Text: "@alice T-1 updated: title (log #2)"})
	if len(woke) != 1 || !strings.Contains(woke[0], "assigned you T-1") {
		t.Fatalf("notifications = %v; only the assignment may wake", woke)
	}
	got := s.inboxRead()
	if !strings.Contains(got, "[/team board] @alice assigned you") || !strings.Contains(got, "[/team board] @alice T-1 updated") {
		t.Fatalf("both events are in the inbox:\n%s", got)
	}
}

func TestBoardLineIsOneLine(t *testing.T) {
	if got := boardLine("team", "a\nb   c"); got != "[/team board] a b c" {
		t.Fatalf("boardLine = %q", got)
	}
}
