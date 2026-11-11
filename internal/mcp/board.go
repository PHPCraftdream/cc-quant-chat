package mcp

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/board"
	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
)

// The task board tools: task, sprint and board, each with an `op`, to keep their descriptions
// (which ride in every session's context) short. The broker does the work; answers are the
// short texts it renders.

// ref is a task, sprint or log number as models write it: 12, "12", "T-12", "#12".
type ref string

func (r *ref) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*r = ref(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("want a number or a string like T-12")
	}
	*r = ref(n.String())
	return nil
}

type boardToolArgs struct {
	Chat        string          `json:"chat"`
	Op          string          `json:"op"`
	ID          ref             `json:"id"`
	Title       *string         `json:"title"`
	Description *string         `json:"description"`
	Assignee    *string         `json:"assignee"`
	Priority    *int            `json:"priority"`
	Sprint      *ref            `json:"sprint"`
	Status      string          `json:"status"`
	Text        string          `json:"text"`
	Mine        bool            `json:"mine"`
	Done        bool            `json:"done"`
	Name        *string         `json:"name"`
	Goal        *string         `json:"goal"`
	Carry       *ref            `json:"carry"`
	LogID       ref             `json:"log_id"`
	Force       bool            `json:"force"`
	Limit       int             `json:"limit"`
	Before      int64           `json:"before"`
	Workflow    json.RawMessage `json:"workflow"`
}

// sprintNumber: S-3, 3 or 0/"backlog" (the backlog is 0).
func sprintNumber(r ref) (int64, error) {
	switch strings.ToLower(strings.TrimSpace(string(r))) {
	case "", "backlog", "none", "0":
		return 0, nil
	}
	return board.ParseSprintRef(string(r))
}

func taskNumber(r ref) (int64, error) {
	if strings.TrimSpace(string(r)) == "" {
		return 0, fmt.Errorf("id is required (a task like T-12)")
	}
	return board.ParseTaskRef(string(r))
}

var boardOps = map[string][]string{
	"task":   {"create", "update", "move", "delete", "comment", "comments", "get", "list"},
	"sprint": {"create", "update", "start", "close", "list"},
	"board":  {"log", "revert", "workflow_get", "workflow_set"},
}

// build turns a tool call into the broker's board request.
func (a boardToolArgs) build(tool string) (proto.BoardArgs, error) {
	ops := boardOps[tool]
	ok := false
	for _, o := range ops {
		ok = ok || o == a.Op
	}
	if !ok {
		return proto.BoardArgs{}, fmt.Errorf("%s: op is one of %s", tool, strings.Join(ops, ", "))
	}
	out := proto.BoardArgs{Op: tool + "." + a.Op}
	var err error
	needID := func() error {
		if tool == "sprint" {
			out.ID, err = sprintNumber(a.ID)
			if err == nil && out.ID == 0 {
				err = fmt.Errorf("id is required (a sprint like S-3)")
			}
		} else {
			out.ID, err = taskNumber(a.ID)
		}
		return err
	}
	switch out.Op {
	case "task.create":
		out.Title, out.Description, out.Assignee, out.Priority = a.Title, a.Description, a.Assignee, a.Priority
		if a.Sprint != nil {
			n, err := sprintNumber(*a.Sprint)
			if err != nil {
				return out, err
			}
			out.Sprint = &n
		}
	case "task.update":
		if err := needID(); err != nil {
			return out, err
		}
		out.Title, out.Description, out.Assignee, out.Priority = a.Title, a.Description, a.Assignee, a.Priority
		if a.Sprint != nil {
			n, err := sprintNumber(*a.Sprint)
			if err != nil {
				return out, err
			}
			out.Sprint = &n
		}
	case "task.move":
		out.Status = a.Status
		return out, needID()
	case "task.delete", "task.get":
		return out, needID()
	case "task.comment":
		out.Text = a.Text
		return out, needID()
	case "task.comments":
		out.Limit, out.Before = a.Limit, a.Before
		return out, needID()
	case "task.list":
		if a.Sprint != nil {
			out.SprintFilter = string(*a.Sprint)
		}
		out.Status, out.Mine, out.Done = a.Status, a.Mine, a.Done
		if a.Assignee != nil {
			out.AssigneeFilter = *a.Assignee
		}
	case "sprint.create":
		out.Name, out.Goal = a.Name, a.Goal
	case "sprint.update":
		out.Name, out.Goal = a.Name, a.Goal
		return out, needID()
	case "sprint.start":
		return out, needID()
	case "sprint.close":
		if a.Carry != nil {
			if out.Carry, err = sprintNumber(*a.Carry); err != nil {
				return out, err
			}
		}
		return out, needID()
	case "sprint.list", "board.workflow_get":
	case "board.log":
		out.Limit, out.Before = a.Limit, a.Before
		if strings.TrimSpace(string(a.ID)) != "" {
			out.Entity = "task"
			if out.ID, err = taskNumber(a.ID); err != nil {
				return out, err
			}
		}
	case "board.revert":
		n, err := strconv.ParseInt(strings.TrimPrefix(strings.TrimSpace(string(a.LogID)), "#"), 10, 64)
		if err != nil || n <= 0 {
			return out, fmt.Errorf("log_id is required (the number of a journal entry, see board op=log)")
		}
		out.LogID, out.Force = n, a.Force
	case "board.workflow_set":
		if len(a.Workflow) == 0 {
			return out, fmt.Errorf(`workflow is required: {"statuses":[..],"initial":"..","terminal":[..],"transitions":{"from":["to",..]}}`)
		}
		out.Workflow = a.Workflow
	}
	return out, err
}

// boardTool runs one call of the task, sprint or board tool.
func (s *Session) boardTool(tool string, raw json.RawMessage) (string, error) {
	var a boardToolArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("bad arguments: %v", err)
	}
	args, err := a.build(tool)
	if err != nil {
		return "", err
	}
	cc, err := s.get(a.Chat)
	if err != nil {
		return "", err
	}
	var r proto.BoardReply
	call := func() error { return cc.call("board", &args, &r) }
	if proto.BoardWrite(args.Op) {
		call = func() error { return cc.write("board", args.Op, &args, &r) }
	}
	if err := call(); err != nil {
		if proto.IsCode(err, "unknown_op") || proto.IsCode(err, "not_joined") {
			return "", fmt.Errorf("the running broker is older and has no task board: run `ccqc broker restart`")
		}
		return "", err
	}
	return strings.TrimRight(r.Text, "\n"), nil
}

// boardLine is a board event as the wake-up batch lists it; the plugin-ui cards parse it.
func boardLine(local, text string) string {
	return "[/" + local + " board] " + strings.Join(strings.Fields(text), " ")
}
