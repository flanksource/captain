package cli

import (
	"github.com/flanksource/clicky/task"
	flanksourceContext "github.com/flanksource/commons/context"
)

func (c *chatSession) start(name string, options ...task.Option) task.TypedTask[PromptRunSummary] {
	options = append(options, task.WithSlots(0))
	handle := task.StartTask(name, func(_ flanksourceContext.Context, t *task.Task) (PromptRunSummary, error) {
		return c.run(t)
	}, options...)
	c.mu.Lock()
	c.task = handle.Task
	stopped := c.terminal && c.stream.wasStopped()
	c.mu.Unlock()
	if stopped {
		handle.Cancel()
	}
	return handle
}
