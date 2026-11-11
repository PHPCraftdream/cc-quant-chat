package board

import (
	"encoding/json"
	"fmt"

	"github.com/PHPCraftdream/cc-quant-chat/internal/proto"
	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

var sprintFieldNames = []string{"name", "goal", "state", "started_at", "closed_at", "deleted"}

func sprintField(s store.Sprint, f string) json.RawMessage {
	switch f {
	case "name":
		return raw(s.Name)
	case "goal":
		return raw(s.Goal)
	case "state":
		return raw(s.State)
	case "started_at":
		return raw(s.StartedAt)
	case "closed_at":
		return raw(s.ClosedAt)
	case "deleted":
		return raw(s.Deleted)
	}
	return nil
}

func setSprintField(s *store.Sprint, f string, v json.RawMessage) error {
	var err error
	switch f {
	case "name":
		err = json.Unmarshal(v, &s.Name)
	case "goal":
		err = json.Unmarshal(v, &s.Goal)
	case "state":
		err = json.Unmarshal(v, &s.State)
	case "started_at":
		err = json.Unmarshal(v, &s.StartedAt)
	case "closed_at":
		err = json.Unmarshal(v, &s.ClosedAt)
	case "deleted":
		err = json.Unmarshal(v, &s.Deleted)
	default:
		err = fmt.Errorf("unknown sprint field %q", f)
	}
	return err
}

func sprintFields(s store.Sprint, names ...string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for _, f := range names {
		out[f] = sprintField(s, f)
	}
	return out
}

func loadSprint(tx store.BoardTx, id int64) (store.Sprint, error) {
	s, ok, err := tx.GetSprint(id)
	if err != nil {
		return s, err
	}
	if !ok || s.Deleted {
		return s, errNoSprint(id)
	}
	return s, nil
}

func validSprintText(name, goal string) (string, string, error) {
	name, goal = oneLine(name), oneLine(goal)
	if len(name) > MaxSprintName {
		return "", "", proto.Errf("too_large", "sprint name is %d bytes, max %d", len(name), MaxSprintName)
	}
	if len(goal) > MaxGoal {
		return "", "", proto.Errf("too_large", "sprint goal is %d bytes, max %d", len(goal), MaxGoal)
	}
	return name, goal, nil
}

// CreateSprint adds a planned sprint.
func CreateSprint(c Ctx, name, goal string) (Change, error) {
	name, goal, err := validSprintText(name, goal)
	if err != nil {
		return Change{}, err
	}
	if name == "" {
		return Change{}, proto.Errf("bad_args", "a sprint needs a name")
	}
	id, err := c.Tx.NextSprintID()
	if err != nil {
		return Change{}, err
	}
	s := store.Sprint{ID: id, Name: name, Goal: goal, State: store.SprintPlanned, CreatedAt: c.ts()}
	if err := c.Tx.PutSprint(s); err != nil {
		return Change{}, err
	}
	logID, err := c.log("sprint.create", "sprint", id, nil, sprintFields(s, sprintFieldNames...), 0)
	if err != nil {
		return Change{}, err
	}
	return Change{Text: fmt.Sprintf("%s created %q (log #%d)", SprintRef(id), name, logID), LogID: logID}, nil
}

// UpdateSprint renames a sprint or changes its goal.
func UpdateSprint(c Ctx, id int64, name, goal *string) (Change, error) {
	s, err := loadSprint(c.Tx, id)
	if err != nil {
		return Change{}, err
	}
	n, g := s.Name, s.Goal
	if name != nil {
		n = *name
	}
	if goal != nil {
		g = *goal
	}
	n, g, err = validSprintText(n, g)
	if err != nil {
		return Change{}, err
	}
	if n == "" {
		return Change{}, proto.Errf("bad_args", "a sprint needs a name")
	}
	before, after := map[string]json.RawMessage{}, map[string]json.RawMessage{}
	if n != s.Name {
		before["name"], after["name"] = raw(s.Name), raw(n)
	}
	if g != s.Goal {
		before["goal"], after["goal"] = raw(s.Goal), raw(g)
	}
	if len(after) == 0 {
		return Change{Text: SprintRef(id) + " unchanged"}, nil
	}
	s.Name, s.Goal = n, g
	if err := c.Tx.PutSprint(s); err != nil {
		return Change{}, err
	}
	logID, err := c.log("sprint.update", "sprint", id, before, after, 0)
	if err != nil {
		return Change{}, err
	}
	return Change{Text: fmt.Sprintf("%s updated (log #%d)", SprintRef(id), logID), LogID: logID}, nil
}

// StartSprint makes a planned sprint the active one; another active sprint refuses it.
func StartSprint(c Ctx, id int64) (Change, error) {
	s, err := loadSprint(c.Tx, id)
	if err != nil {
		return Change{}, err
	}
	if s.State != store.SprintPlanned {
		return Change{}, proto.Errf("bad_transition", "%s is %s; only a planned sprint can be started", SprintRef(id), s.State)
	}
	if act, ok, err := c.Tx.ActiveSprint(); err != nil {
		return Change{}, err
	} else if ok {
		return Change{}, proto.Errf("sprint_active", "%s is active; close it first (a chat has one active sprint)", SprintRef(act.ID))
	}
	before := map[string]json.RawMessage{"state": raw(s.State), "started_at": raw(s.StartedAt)}
	s.State, s.StartedAt = store.SprintActive, c.ts()
	if err := c.Tx.PutSprint(s); err != nil {
		return Change{}, err
	}
	logID, err := c.log("sprint.start", "sprint", id, before, map[string]json.RawMessage{"state": raw(s.State), "started_at": raw(s.StartedAt)}, 0)
	if err != nil {
		return Change{}, err
	}
	return Change{Text: fmt.Sprintf("%s started (log #%d)", SprintRef(id), logID), LogID: logID}, nil
}

// CloseSprint closes the active sprint; its unfinished tasks go to carry (0: the backlog) in the
// same journal entry.
func CloseSprint(c Ctx, id, carry int64) (Change, error) {
	s, err := loadSprint(c.Tx, id)
	if err != nil {
		return Change{}, err
	}
	if s.State != store.SprintActive {
		return Change{}, proto.Errf("bad_transition", "%s is %s; only the active sprint can be closed", SprintRef(id), s.State)
	}
	if carry == id {
		return Change{}, proto.Errf("bad_args", "carry must be another sprint or 0 for the backlog")
	}
	if err := checkSprintTarget(c.Tx, carry); err != nil {
		return Change{}, err
	}
	wf, err := workflow(c.Tx)
	if err != nil {
		return Change{}, err
	}
	open, err := c.Tx.ListTasks(store.TaskFilter{Sprint: id, NotStatuses: wf.Terminal})
	if err != nil {
		return Change{}, err
	}
	ids := make([]int64, 0, len(open))
	for _, t := range open {
		t.Sprint, t.UpdatedAt = carry, c.ts()
		if err := c.Tx.PutTask(t); err != nil {
			return Change{}, err
		}
		ids = append(ids, t.ID)
	}
	before := map[string]json.RawMessage{"state": raw(s.State), "closed_at": raw(s.ClosedAt), "carried": raw(ids), "carry_to": raw(id)}
	s.State, s.ClosedAt = store.SprintClosed, c.ts()
	if err := c.Tx.PutSprint(s); err != nil {
		return Change{}, err
	}
	after := map[string]json.RawMessage{"state": raw(s.State), "closed_at": raw(s.ClosedAt), "carried": raw(ids), "carry_to": raw(carry)}
	logID, err := c.log("sprint.close", "sprint", id, before, after, 0)
	if err != nil {
		return Change{}, err
	}
	where := "the backlog"
	if carry != 0 {
		where = SprintRef(carry)
	}
	return Change{Text: fmt.Sprintf("%s closed; %d unfinished task(s) moved to %s (log #%d)", SprintRef(id), len(ids), where, logID), LogID: logID}, nil
}
