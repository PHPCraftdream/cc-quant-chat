package board

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// ValidateWorkflow checks a process on its own: names, counts, and that every status it mentions
// is declared.
func ValidateWorkflow(w store.Workflow) error {
	if n := len(w.Statuses); n == 0 || n > MaxStatuses {
		return proto.Errf("bad_workflow", "a process has 1..%d statuses, got %d", MaxStatuses, n)
	}
	known := map[string]bool{}
	for _, s := range w.Statuses {
		if !proto.ValidName(s) {
			return proto.Errf("bad_workflow", "status %q: want [a-z0-9-]{1,32}", s)
		}
		if known[s] {
			return proto.Errf("bad_workflow", "status %q is listed twice", s)
		}
		known[s] = true
	}
	if !known[w.Initial] {
		return proto.Errf("bad_workflow", "initial status %q is not among the statuses", w.Initial)
	}
	for _, s := range w.Terminal {
		if !known[s] {
			return proto.Errf("bad_workflow", "terminal status %q is not among the statuses", s)
		}
	}
	for from, tos := range w.Transitions {
		if !known[from] {
			return proto.Errf("bad_workflow", "transitions from %q: not a status", from)
		}
		for _, to := range tos {
			if !known[to] {
				return proto.Errf("bad_workflow", "transition %s → %q: not a status", from, to)
			}
		}
	}
	return nil
}

// orphans: live tasks whose status the process does not have, as "status (n)" lines.
func orphans(tx store.BoardTx, w store.Workflow) (int, []string, error) {
	counts, err := tx.CountByStatus(-1)
	if err != nil {
		return 0, nil, err
	}
	total := 0
	var names []string
	for s, n := range counts {
		if !contains(w.Statuses, s) {
			total += n
			names = append(names, fmt.Sprintf("%s (%d)", s, n))
		}
	}
	sort.Strings(names)
	return total, names, nil
}

// putWorkflow validates, stores and journals a process (kind "workflow.set" or "revert").
func putWorkflow(c Ctx, w store.Workflow, kind string, reverts int64) (int64, error) {
	if err := ValidateWorkflow(w); err != nil {
		return 0, err
	}
	if n, names, err := orphans(c.Tx, w); err != nil {
		return 0, err
	} else if n > 0 {
		return 0, proto.Errf("orphaned_statuses", "%d task(s) have statuses the new process lacks: %s; move them first or keep those statuses", n, strings.Join(names, ", "))
	}
	old, err := workflow(c.Tx)
	if err != nil {
		return 0, err
	}
	if err := c.Tx.PutWorkflow(w, c.ts()); err != nil {
		return 0, err
	}
	return c.log(kind, "workflow", 0, map[string]json.RawMessage{"workflow": raw(old)}, map[string]json.RawMessage{"workflow": raw(w)}, reverts)
}

// SetWorkflow replaces the board's process as a whole.
func SetWorkflow(c Ctx, w store.Workflow) (Change, error) {
	logID, err := putWorkflow(c, w, "workflow.set", 0)
	if err != nil {
		return Change{}, err
	}
	return Change{Text: fmt.Sprintf("process set: %s (log #%d)", strings.Join(w.Statuses, ", "), logID), LogID: logID}, nil
}

// GetWorkflow returns the process and whether it is the default one.
func GetWorkflow(tx store.BoardTx) (store.Workflow, bool, error) {
	w, ok, err := tx.GetWorkflow()
	if err != nil {
		return w, false, err
	}
	if !ok {
		return DefaultWorkflow(), true, nil
	}
	return w, false, nil
}

// FormatWorkflow shows a process in one compact block.
func FormatWorkflow(w store.Workflow, isDefault bool) string {
	var b strings.Builder
	if isDefault {
		b.WriteString("process (default):\n")
	} else {
		b.WriteString("process:\n")
	}
	fmt.Fprintf(&b, "initial: %s; terminal: %s\n", w.Initial, strings.Join(w.Terminal, ", "))
	for _, s := range w.Statuses {
		fmt.Fprintf(&b, "  %s → %s\n", s, strings.Join(w.Transitions[s], ", "))
	}
	return b.String()
}
