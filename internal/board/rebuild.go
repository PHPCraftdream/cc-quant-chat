package board

import (
	"encoding/json"
	"sort"

	"github.com/PHPCraftdream/cc-quant-chat/internal/store"
)

// Rebuild replays a journal (oldest first) into the state of tasks and comments. The rule it
// serves: no change to the board is ever made outside a journal row, so the tables the board
// reads (board_tasks, board_comments) are only a cache of the journal. A test keeps it true.
func Rebuild(entries []store.LogEntry) (map[int64]store.Task, map[int64]store.Comment) {
	tasks := map[int64]store.Task{}
	comments := map[int64]store.Comment{}
	for _, e := range entries {
		switch e.Entity {
		case "task":
			if _, isComment := e.After["comment"]; isComment {
				rebuildComment(comments, e)
				continue
			}
			t, ok := tasks[e.EntityID]
			if e.Kind == "task.create" && !ok {
				t = store.Task{ID: e.EntityID, CreatedBy: e.Actor, CreatedAt: e.TS}
			}
			for f, v := range e.After {
				_ = setTaskField(&t, f, v)
			}
			t.UpdatedAt = e.TS
			tasks[e.EntityID] = t
		case "sprint":
			// Closing a sprint (or undoing that) moves the carried tasks.
			rawIDs, has := e.After["carried"]
			if !has {
				continue
			}
			var ids []int64
			var to int64
			_ = json.Unmarshal(rawIDs, &ids)
			_ = json.Unmarshal(e.After["carry_to"], &to)
			for _, id := range ids {
				if t, ok := tasks[id]; ok && !t.Deleted {
					t.Sprint, t.UpdatedAt = to, e.TS
					tasks[id] = t
				}
			}
		}
	}
	return tasks, comments
}

func rebuildComment(comments map[int64]store.Comment, e store.LogEntry) {
	var id int64
	if json.Unmarshal(e.After["comment"], &id) != nil {
		return
	}
	cm := comments[id]
	if e.Kind == "task.comment" {
		cm = store.Comment{ID: id, TaskID: e.EntityID, TS: e.TS, Author: e.Actor, AuthorOwner: e.ActorOwner,
			Origin: e.Origin, Lamport: e.Lamport}
		_ = json.Unmarshal(e.After["text"], &cm.Text)
	}
	if v, has := e.After["hidden"]; has {
		_ = json.Unmarshal(v, &cm.Deleted)
	}
	comments[id] = cm
}

// SortedTaskIDs lists the keys of a rebuilt task map, for comparisons.
func SortedTaskIDs(m map[int64]store.Task) []int64 {
	ids := make([]int64, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
