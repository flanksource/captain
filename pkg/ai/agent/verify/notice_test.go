package verify

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude/tools"
)

// TestVerdictsReachTheRunsEventStream drives the hook through the real Runner,
// because the link under test is the one between the verifier and the run's
// event stream. Verification is the loop's definition of done and the only
// participant that used to be silent: the model's turns stream, but a verify
// command's output went into the retry prompt and nowhere else, so a run showed
// a turn, then a long pause, then another turn, with no record of what the check
// had said in between.
func TestVerdictsReachTheRunsEventStream(t *testing.T) {
	for _, test := range []struct {
		name     string
		command  string
		wantKind api.EventKind
		contains []string
	}{{
		name:     "a passing command still reports, or a green run says nothing at all",
		command:  "true",
		wantKind: api.EventVerified,
		contains: []string{"passed in ", "verify:true"},
	}, {
		name:     "a failing command reports its reason and its output",
		command:  "echo 'Please Enter a Valid Cover Amount'; exit 1",
		wantKind: api.EventVerifyFailed,
		contains: []string{"failed in ", "sh failed", "Please Enter a Valid Cover Amount"},
	}} {
		t.Run(test.name, func(t *testing.T) {
			hooks, err := HooksFor(context.Background(),
				&api.Workflow{Verify: &api.Verify{Commands: []string{test.command}}}, Options{})
			if err != nil {
				t.Fatalf("hooks: %v", err)
			}
			var streamed []ai.Event
			runner := &agent.Runner[string]{
				Provider:      &silentProvider{},
				Cwd:           t.TempDir(),
				Request:       ai.Request{Prompt: api.Prompt{User: "fix it"}},
				Hooks:         hooks,
				MaxIterations: 1,
				OnEvent: func(_ int, ev ai.Event) {
					if ev.Kind == api.EventVerified || ev.Kind == api.EventVerifyFailed {
						streamed = append(streamed, ev)
					}
				},
			}
			res, runErr := runner.Run(context.Background())
			if runErr != nil {
				t.Fatalf("run: %v", runErr)
			}

			if len(streamed) != 1 {
				t.Fatalf("streamed verdicts = %+v, want exactly one", streamed)
			}
			got := streamed[0]
			// The kind is the verdict: a consumer selects on it instead of
			// reading the sentence to find out whether anything passed.
			if got.Kind != test.wantKind {
				t.Errorf("kind = %q, want %q", got.Kind, test.wantKind)
			}
			if got.Success != (test.wantKind == api.EventVerified) {
				t.Errorf("Success = %t, want it to agree with kind %q", got.Success, got.Kind)
			}
			// The check's identity and wall clock travel as fields, not only in
			// the prose, so a dashboard need not parse them back out.
			if !strings.HasPrefix(got.Tool, "verify:") {
				t.Errorf("Tool = %q, want the hook's name", got.Tool)
			}
			if got.Duration <= 0 {
				t.Errorf("Duration = %s, want the verifier's wall clock", got.Duration)
			}
			for _, want := range test.contains {
				if !strings.Contains(got.Text, want) {
					t.Errorf("verdict text %q does not contain %q", got.Text, want)
				}
			}

			// The stream dies with the terminal; the buffered copy is what a
			// caller persists into the session transcript once the run's session
			// id is known, so the two must not drift — in text or in kind.
			notices := res.Response.Workspace.Notices
			if len(notices) != 1 {
				t.Fatalf("buffered notices = %+v, want exactly one", notices)
			}
			if notices[0].Text != got.Text {
				t.Errorf("buffered notice = %q, want the streamed text %q", notices[0].Text, got.Text)
			}
			if notices[0].Kind != test.wantKind {
				t.Errorf("buffered notice kind = %q, want %q — a stored verdict must stay selectable",
					notices[0].Kind, test.wantKind)
			}
		})
	}
}

// TestVerdictSurvivesTheTranscriptPreview pins why the verdict leads the notice
// and the hook's name trails it. A transcript row shows only the first
// MessagePreviewChars runes, and the cmd factory names a hook after the entire
// shell command it runs — so naming it first spends the whole preview on a
// command line the reader already knows, and the row never says whether
// anything passed.
func TestVerdictSurvivesTheTranscriptPreview(t *testing.T) {
	command := "oipa-cli test " + strings.Repeat("fixtures/some/deeply/nested/fixture.yaml ", 6)
	hc := &agent.HookContext{
		Context:  context.Background(),
		Request:  &ai.Request{},
		Response: &ai.Response{Workspace: &api.Workspace{Cwd: t.TempDir()}},
	}
	New("verify:"+command, FuncVerifier(func(context.Context, string, []string) (Verdict, error) {
		return Verdict{OK: false, Reason: "sh failed", Feedback: "Please Enter a Valid Cover Amount"}, nil
	})).notify(hc, Verdict{OK: false, Reason: "sh failed", Feedback: "boom"}, 5*time.Minute)

	notices := hc.Workspace().Notices
	if len(notices) != 1 {
		t.Fatalf("notices = %+v, want exactly one", notices)
	}
	preview := []rune(notices[0].Text)
	if len(preview) > tools.MessagePreviewChars {
		preview = preview[:tools.MessagePreviewChars]
	}
	for _, want := range []string{"failed in 5m0s", "sh failed"} {
		if !strings.Contains(string(preview), want) {
			t.Errorf("the first %d runes (%q) must still say %q",
				tools.MessagePreviewChars, string(preview), want)
		}
	}
}

// TestVerifierErrorIsNotNotified pins Notify's contract: it is purely
// informational, and a hook that failed reports by returning an error rather
// than narrating one. A verifier that could not run at all aborts the run, and
// the abort is the report.
func TestVerifierErrorIsNotNotified(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	hc := &agent.HookContext{
		Context:  cancelled,
		Request:  &ai.Request{},
		Response: &ai.Response{Workspace: &api.Workspace{Cwd: t.TempDir()}},
	}
	_, err := New("verify:true", &CmdVerifier{Cmd: "true"}).Verify(hc)

	if err == nil {
		t.Fatal("a cancelled run must surface the cancellation, not a verdict")
	}
	if notices := hc.Workspace().Notices; len(notices) != 0 {
		t.Errorf("notices = %+v, want none for a verifier that could not reach a verdict", notices)
	}
}

// silentProvider is an agent that edits nothing and says nothing, so each test's
// only events are the ones its verifier produces.
type silentProvider struct{}

func (*silentProvider) GetModel() string       { return "fake" }
func (*silentProvider) GetRuntime() ai.Runtime { return ai.RuntimeOf(ai.Anthropic, ai.ModeAgent) }

func (*silentProvider) Execute(context.Context, ai.Request) (*ai.Response, error) {
	return &ai.Response{}, nil
}

func (*silentProvider) ExecuteStream(context.Context, ai.Request) (<-chan ai.Event, error) {
	ch := make(chan ai.Event, 1)
	ch <- ai.Event{Kind: ai.EventResult, Success: true}
	close(ch)
	return ch, nil
}
