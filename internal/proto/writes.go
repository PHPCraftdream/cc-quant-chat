package proto

// The ops of the board and of the document tree that write. Writes are signed by their author;
// a stopping broker refuses them like other changes.
var boardWrites = map[string]bool{
	"task.create": true, "task.update": true, "task.move": true, "task.delete": true, "task.comment": true,
	"sprint.create": true, "sprint.update": true, "sprint.start": true, "sprint.close": true,
	"board.revert": true, "board.workflow_set": true,
}

var docWrites = map[string]bool{"put": true, "append": true, "move": true, "delete": true, "revert": true}

// BoardWrite reports whether a board op changes the board.
func BoardWrite(op string) bool { return boardWrites[op] }

// DocWrite reports whether a document op changes the tree.
func DocWrite(op string) bool { return docWrites[op] }
