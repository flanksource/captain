package session

import (
	"github.com/flanksource/captain/pkg/claude/tools"
)

// todoToolNames are the tool calls that carry an agent task list. Codex's
// update_plan is normalized to TodoWrite by NormalizeCodexToolCall, but the raw
// name is accepted too so this works on un-normalized input.
var todoToolNames = map[string]struct{}{
	"TodoWrite":   {},
	"update_plan": {},
}

// todosFromInput reads a task list out of a tool call's input. Claude keys it
// "todos", Codex "plan".
func todosFromInput(input map[string]any) []tools.TodoItem {
	raw := input["todos"]
	if raw == nil {
		raw = input["plan"]
	}
	return tools.TodoItems(raw)
}

// latestTodos returns the task list from the most recent task-list call in the
// session. A task list is current state rather than an event log, so a later
// call fully supersedes an earlier one; scanning backwards and stopping at the
// first non-empty payload gives latest-wins without accumulating history.
//
// The accessor keeps this shared between the Claude and Codex builders, whose
// tool-use types differ but both expose a name and an input map.
func latestTodos[T any](uses []T, nameAndInput func(T) (string, map[string]any)) []tools.TodoItem {
	for i := len(uses) - 1; i >= 0; i-- {
		name, input := nameAndInput(uses[i])
		if _, ok := todoToolNames[name]; !ok {
			continue
		}
		if items := todosFromInput(input); len(items) > 0 {
			return items
		}
	}
	return nil
}
