package board

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// ParseTaskRef reads T-12, t12, #12 or 12.
func ParseTaskRef(s string) (int64, error) { return parseRef(s, "T") }

// ParseSprintRef reads S-3, s3 or 3.
func ParseSprintRef(s string) (int64, error) { return parseRef(s, "S") }

func parseRef(s, letter string) (int64, error) {
	t := strings.TrimSpace(s)
	t = strings.TrimPrefix(strings.TrimPrefix(t, "#"), strings.ToLower(letter))
	t = strings.TrimPrefix(strings.TrimPrefix(t, letter), "-")
	n, err := strconv.ParseInt(t, 10, 64)
	if err != nil || n < 0 {
		return 0, proto.Errf("bad_args", "%q is not a %s-number (want %s-12 or 12)", s, strings.ToLower(letter), letter)
	}
	return n, nil
}

// ListArgs selects what `task list` shows. By default: the active sprint (the backlog when none
// is active), without finished tasks.
type ListArgs struct {
	Sprint   string // "", "active", "backlog", "all", or a sprint number
	Status   string
	Assignee string
	Mine     bool
	Done     bool // include finished (terminal) tasks
}

func shortTS(ts string) string {
	if len(ts) >= 16 {
		return strings.Replace(ts[5:16], "T", " ", 1)
	}
	return ts
}

func countsLine(wf store.Workflow, counts map[string]int) string {
	var parts []string
	for _, s := range wf.Statuses {
		if n := counts[s]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", s, n))
		}
	}
	for s, n := range counts { // statuses that are no longer in the process
		if !contains(wf.Statuses, s) {
			parts = append(parts, fmt.Sprintf("%s %d", s, n))
		}
	}
	if len(parts) == 0 {
		return "empty"
	}
	return strings.Join(parts, " · ")
}

func taskLine(t store.Task) string {
	who := "-"
	if t.Assignee != "" {
		who = "@" + t.Assignee
	}
	return fmt.Sprintf("%s %s %s p%d %s", TaskRef(t.ID), t.Status, who, t.Priority, clip(t.Title, TitleInList))
}

// List renders a short listing: a heading with the sprint and counts, one line per task (no
// descriptions), at most ListLines of them.
func List(tx store.BoardTx, actor string, a ListArgs) (string, error) {
	wf, err := workflow(tx)
	if err != nil {
		return "", err
	}
	scope, heading := int64(0), "Backlog"
	switch strings.ToLower(strings.TrimSpace(a.Sprint)) {
	case "", "active":
		act, ok, err := tx.ActiveSprint()
		if err != nil {
			return "", err
		}
		if ok {
			scope, heading = act.ID, fmt.Sprintf("Sprint %s %q (active)", SprintRef(act.ID), act.Name)
		} else if strings.TrimSpace(a.Sprint) != "" {
			return "", proto.Errf("no_active_sprint", "no sprint is active; use sprint=backlog or sprint=all")
		}
	case "backlog":
	case "all":
		scope, heading = -1, "All tasks"
	default:
		id, err := ParseSprintRef(a.Sprint)
		if err != nil {
			return "", err
		}
		sp, ok, err := tx.GetSprint(id)
		if err != nil {
			return "", err
		}
		if !ok || sp.Deleted {
			return "", errNoSprint(id)
		}
		scope, heading = id, fmt.Sprintf("Sprint %s %q (%s)", SprintRef(id), sp.Name, sp.State)
	}
	counts, err := tx.CountByStatus(scope)
	if err != nil {
		return "", err
	}
	f := store.TaskFilter{Sprint: scope, Assignee: a.Assignee}
	if a.Mine {
		f.Assignee = actor
	}
	switch {
	case a.Status != "":
		f.Statuses = []string{a.Status}
	case !a.Done:
		f.NotStatuses = wf.Terminal
	}
	tasks, err := tx.ListTasks(f)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\n", heading, countsLine(wf, counts))
	for i, t := range tasks {
		if i == ListLines {
			fmt.Fprintf(&b, "…and %d more (narrow with status, assignee or sprint)\n", len(tasks)-ListLines)
			break
		}
		b.WriteString(taskLine(t) + "\n")
	}
	if len(tasks) == 0 {
		b.WriteString("no tasks match\n")
	}
	return b.String(), nil
}

// Get renders one task in full with the last five journal entries about it.
func Get(tx store.BoardTx, id int64) (string, error) {
	t, ok, err := tx.GetTask(id)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errNoTask(id)
	}
	who := "unassigned"
	if t.Assignee != "" {
		who = "@" + t.Assignee
	}
	deleted := ""
	if t.Deleted {
		deleted = " (deleted)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %q%s\n%s · p%d · %s · %s\n", TaskRef(t.ID), t.Title, deleted, t.Status, t.Priority, who, sprintLabel(t.Sprint))
	fmt.Fprintf(&b, "created by @%s %s, updated %s\n", t.CreatedBy, shortTS(t.CreatedAt), shortTS(t.UpdatedAt))
	if t.Description == "" {
		b.WriteString("(no description)\n")
	} else {
		b.WriteString(t.Description + "\n")
	}
	log, err := tx.LogFor("task", id, 0, 5)
	if err != nil {
		return "", err
	}
	if len(log) > 0 {
		b.WriteString("journal (newest first):\n")
		for _, e := range log {
			b.WriteString("  " + logLine(e) + "\n")
		}
	}
	return b.String(), nil
}

// Sprints lists the sprints, one line each with status counts.
func Sprints(tx store.BoardTx) (string, error) {
	wf, err := workflow(tx)
	if err != nil {
		return "", err
	}
	list, err := tx.ListSprints()
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "no sprints (the backlog: task list sprint=backlog)\n", nil
	}
	var b strings.Builder
	for _, s := range list {
		counts, err := tx.CountByStatus(s.ID)
		if err != nil {
			return "", err
		}
		goal := ""
		if s.Goal != "" {
			goal = " — " + clip(s.Goal, 60)
		}
		fmt.Fprintf(&b, "%s %s %q: %s%s\n", SprintRef(s.ID), s.State, s.Name, countsLine(wf, counts), goal)
	}
	return b.String(), nil
}

func logLine(e store.LogEntry) string {
	who := e.Actor
	if e.ActorOwner {
		who = "OWNER " + who
	}
	what := ""
	switch e.Entity {
	case "task":
		what = TaskRef(e.EntityID)
	case "sprint":
		what = SprintRef(e.EntityID)
	default:
		what = e.Entity
	}
	var parts []string
	for _, f := range sortedFields(e.After) {
		if e.Kind == "workflow.set" || f == "workflow" {
			parts = append(parts, "process")
			continue
		}
		if e.Kind == "task.create" || e.Kind == "sprint.create" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %s→%s", f, short(e.Before[f]), short(e.After[f])))
	}
	rev := ""
	if e.Reverts > 0 {
		rev = fmt.Sprintf(" (of #%d)", e.Reverts)
	}
	return strings.TrimSpace(fmt.Sprintf("#%d %s %s %s %s%s %s", e.ID, shortTS(e.TS), who, e.Kind, what, rev, strings.Join(parts, ", ")))
}

// Log lists journal entries newest first: of the whole board (taskID 0 and sprintID 0) or of one
// task. before pages back (entries with a smaller id).
func Log(tx store.BoardTx, entity string, entityID int64, limit int, before int64) (string, error) {
	if limit <= 0 {
		limit = LogDefault
	}
	if limit > LogMax {
		limit = LogMax
	}
	entries, err := tx.LogFor(entity, entityID, before, limit)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "the journal is empty here\n", nil
	}
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(logLine(e) + "\n")
	}
	if len(entries) == limit {
		fmt.Fprintf(&b, "(older: before=%d)\n", entries[len(entries)-1].ID)
	}
	return b.String(), nil
}
