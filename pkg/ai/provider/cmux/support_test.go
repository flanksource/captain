package cmux

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
)

// testSurface is the fixed workspace/surface ref the driver tests target.
var testSurface = WorkspaceRef{WorkspaceID: "workspace:ws1", SurfaceID: "surface:sf1"}

// completedSessionLine is one assistant end_turn entry; a tailer that reads it
// reports the session complete.
const completedSessionLine = `{"type":"assistant","sessionId":"s","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"done"}]}}`

// newTestRun builds a *run wired to a recording cmux Runner, with a no-op event
// sink, so the driver methods can be exercised without a live cmux instance.
func newTestRun(cfg runConfig, runner Runner) *run {
	client := NewClient("")
	client.Runner = runner
	return &run{client: client, cfg: cfg, emit: func(ai.Event) {}}
}

// recordingRun is a *run whose emitted events are captured for assertions.
func recordingRun(cfg runConfig, runner Runner) (*run, *[]ai.Event) {
	r := newTestRun(cfg, runner)
	var events []ai.Event
	r.emit = func(ev ai.Event) { events = append(events, ev) }
	return r, &events
}

// fakeClaudeHome redirects the claude projects directory (resolved from $HOME) to
// a temp dir so cmux runs read and write session logs inside the test sandbox
// instead of the developer's real ~/.claude.
func fakeClaudeHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

// sessionLogFile resolves the session log a cmux run tails for sessionID under
// workDir, creating its parent directory tree.
func sessionLogFile(t *testing.T, workDir, sessionID string) string {
	t.Helper()
	path, err := SessionLogPath(workDir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
