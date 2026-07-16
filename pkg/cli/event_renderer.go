package cli

import (
	"os"

	"github.com/flanksource/captain/pkg/ai"
)

// NewEventRenderer returns the canonical stateful terminal callback used for
// live Captain events. It shares the same history-backed row renderer as the
// Captain CLI, including session boundaries and structured tool rows.
func NewEventRenderer(output *os.File) func(int, ai.Event) {
	renderer := newLineRenderer(output, 8)
	return func(_ int, event ai.Event) {
		renderEvent(output, renderer, event)
	}
}
