package commit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
)

// TestNoticesReachTheRunsEventStream drives the hook through the real Runner
// rather than calling Post directly, because the link that used to be missing is
// precisely the one between a hook and the run's event stream: a hook could
// record a commit on the workspace but had no way to say so while the run was
// still going. Everything downstream — the terminal renderer, the dashboard's
// SSE frames — reads that stream, so a notice that never reaches it is invisible
// no matter how well the renderers work.
func TestNoticesReachTheRunsEventStream(t *testing.T) {
	dir := newRepo(t)
	target := filepath.Join(dir, "fix.go")

	var streamed []string
	runner := &agent.Runner[string]{
		Provider: &writeOnceProvider{path: target},
		Repo:     dir,
		Cwd:      dir,
		Request:  ai.Request{Prompt: api.Prompt{User: "fix it"}},
		Hooks: []any{New(api.Commit{
			On: api.CommitOnTurn, Mode: api.CommitModeCommit, Message: "fix: the thing",
		})},
		OnEvent: func(_ int, ev ai.Event) {
			if ev.Kind == ai.EventSystem && ev.Text != "" {
				streamed = append(streamed, ev.Text)
			}
		},
	}
	res, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	commits := res.Response.Workspace.Commits
	if len(commits) != 1 {
		t.Fatalf("commits = %+v, want exactly one", commits)
	}
	want := "[post-turn] committed " + commits[0].SHA[:7] + ": fix: the thing"
	if len(streamed) != 2 || streamed[0] != "[post-turn] committing 1 file(s)" || streamed[1] != want {
		t.Errorf("streamed notices = %q, want the committing/committed pair ending in %q", streamed, want)
	}

	// The same lines are also buffered for the caller to persist once the run's
	// session id is known; the stream alone dies with the terminal.
	var buffered []string
	for _, notice := range res.Response.Workspace.Notices {
		buffered = append(buffered, notice.Text)
	}
	if strings.Join(buffered, "\n") != strings.Join(streamed, "\n") {
		t.Errorf("buffered notices = %q, want the same lines as the stream %q", buffered, streamed)
	}
}

// writeOnceProvider is an agent that edits one file on its first turn and
// nothing afterwards, so exactly one commit is cut.
type writeOnceProvider struct {
	path string
	runs int
}

func (p *writeOnceProvider) GetModel() string       { return "fake" }
func (p *writeOnceProvider) GetRuntime() ai.Runtime { return ai.RuntimeOf(ai.Anthropic, ai.ModeAgent) }
func (p *writeOnceProvider) Execute(context.Context, ai.Request) (*ai.Response, error) {
	return &ai.Response{}, nil
}

func (p *writeOnceProvider) ExecuteStream(_ context.Context, _ ai.Request) (<-chan ai.Event, error) {
	first := p.runs == 0
	p.runs++
	ch := make(chan ai.Event, 4)
	go func() {
		defer close(ch)
		if first {
			if err := os.WriteFile(p.path, []byte("package main\n"), 0o600); err != nil {
				ch <- ai.Event{Kind: ai.EventError, Error: err.Error()}
				return
			}
			ch <- ai.Event{Kind: ai.EventToolUse, Tool: "Write", Input: map[string]any{"file_path": p.path}}
		}
		ch <- ai.Event{Kind: ai.EventResult, Success: true}
	}()
	return ch, nil
}
