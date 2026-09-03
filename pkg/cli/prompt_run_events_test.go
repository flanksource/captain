package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/session"
	"github.com/segmentio/encoding/json"
)

type fakeTaskSink struct{}

func (fakeTaskSink) SetDescription(string)         {}
func (fakeTaskSink) SetProgress(int, int)          {}
func (fakeTaskSink) Infof(string, ...interface{})  {}
func (fakeTaskSink) Warnf(string, ...interface{})  {}
func (fakeTaskSink) Errorf(string, ...interface{}) {}

func collectEntries(model, backend string, events ...ai.Event) []session.Message {
	var out []session.Message
	acc := newPromptEventAccumulator(func(m session.Message) { out = append(out, m) }, fakeTaskSink{}, model, backend)
	for _, ev := range events {
		acc.handle(0, ev)
	}
	return out
}

// partText decodes a tool part's JSON-string Output back to plain text.
func partText(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func TestPromptRunAccumulator_CoalescesTextDeltas(t *testing.T) {
	msgs := collectEntries("m", "b",
		ai.Event{Kind: ai.EventText, Text: "Hello "},
		ai.Event{Kind: ai.EventText, Text: "world"},
	)
	if len(msgs) != 2 {
		t.Fatalf("want one frame per delta (2), got %d", len(msgs))
	}
	if msgs[0].ID != msgs[1].ID {
		t.Fatalf("text deltas must share an id: %q vs %q", msgs[0].ID, msgs[1].ID)
	}
	if got := msgs[1].Parts[0].Text; got != "Hello world" {
		t.Fatalf("coalesced text = %q, want %q", got, "Hello world")
	}
}

func TestPromptRunAccumulator_EmitsStructuredResultAsFreshMessage(t *testing.T) {
	msgs := collectEntries("m", "b",
		ai.Event{Kind: ai.EventText, Text: "narrative"},
		ai.Event{Kind: ai.EventResult, StructuredData: json.RawMessage(`{"answer":"42"}`)},
	)
	if len(msgs) != 2 {
		t.Fatalf("want narrative and one structured result frame, got %d", len(msgs))
	}
	if msgs[0].ID == msgs[1].ID {
		t.Fatalf("structured result must start a fresh message")
	}
	if got := msgs[1].Parts[0].Text; got != `{"answer":"42"}` {
		t.Fatalf("structured result text = %q, want coherent JSON", got)
	}
}

func TestPromptRunAccumulator_ThinkingAndTextAreSeparateTurns(t *testing.T) {
	msgs := collectEntries("m", "b",
		ai.Event{Kind: ai.EventThinking, Text: "hmm"},
		ai.Event{Kind: ai.EventText, Text: "answer"},
	)
	if len(msgs) != 2 {
		t.Fatalf("want 2 frames, got %d", len(msgs))
	}
	if msgs[0].ID == msgs[1].ID {
		t.Fatalf("thinking and text must not share an id")
	}
	if msgs[0].Parts[0].Type != session.PartReasoning {
		t.Fatalf("first frame part type = %q, want reasoning", msgs[0].Parts[0].Type)
	}
	if msgs[1].Parts[0].Type != session.PartText {
		t.Fatalf("second frame part type = %q, want text", msgs[1].Parts[0].Type)
	}
}

func TestPromptRunAccumulator_CorrelatesToolResult(t *testing.T) {
	msgs := collectEntries("m", "claude",
		ai.Event{Kind: ai.EventToolUse, Tool: "Bash", Input: map[string]any{"command": "ls"}, ToolCallID: "t1"},
		ai.Event{Kind: ai.EventToolResult, Text: "file.txt", Success: true, ToolCallID: "t1"},
	)
	if len(msgs) != 2 {
		t.Fatalf("want 2 frames (use + result), got %d", len(msgs))
	}
	for i, m := range msgs {
		if m.ID != "t1" {
			t.Fatalf("frame %d id = %q, want tool call id t1", i, m.ID)
		}
		if m.Parts[0].Type != session.PartTool {
			t.Fatalf("frame %d part type = %q, want a tool part", i, m.Parts[0].Type)
		}
	}
	if got := partText(msgs[1].Parts[0].Output); got != "file.txt" {
		t.Fatalf("tool output = %q, want file.txt", got)
	}
	if msgs[1].Parts[0].ToolName != "Bash" {
		t.Fatalf("result frame must carry original tool name, got %q", msgs[1].Parts[0].ToolName)
	}
	if msgs[1].Parts[0].State != session.ToolStateOutputAvailable {
		t.Fatalf("result state = %q, want output-available", msgs[1].Parts[0].State)
	}
}

func TestPromptRunAccumulator_MarksFailedToolResult(t *testing.T) {
	msgs := collectEntries("m", "claude",
		ai.Event{Kind: ai.EventToolUse, Tool: "Bash", ToolCallID: "t1"},
		ai.Event{Kind: ai.EventToolResult, Text: "boom", Success: false, ToolCallID: "t1"},
	)
	last := msgs[len(msgs)-1].Parts[0]
	if resp := partText(last.Output); !strings.HasPrefix(resp, "[error]") {
		t.Fatalf("failed tool output = %q, want [error] prefix", resp)
	}
	if last.State != session.ToolStateOutputError {
		t.Fatalf("failed tool state = %q, want output-error", last.State)
	}
}

func TestPromptRunAccumulator_EmitsErrorFrame(t *testing.T) {
	msgs := collectEntries("m", "b",
		ai.Event{Kind: ai.EventError, Error: "rate limited"},
	)
	if len(msgs) != 1 {
		t.Fatalf("want 1 error frame, got %d", len(msgs))
	}
	if got := msgs[0].Parts[0].Text; got != "rate limited" {
		t.Fatalf("error frame text = %q, want rate limited", got)
	}
}

// A hook narrating what it did between turns arrives as an EventSystem carrying
// text rather than a session id. It has to break out of the assistant's
// in-flight turn, not be appended to it, or a commit line lands inside the
// model's prose.
func TestPromptRunAccumulator_EmitsHookNoticeAsItsOwnSystemFrame(t *testing.T) {
	msgs := collectEntries("m", "b",
		ai.Event{Kind: ai.EventText, Text: "done, committing now"},
		ai.Event{Kind: ai.EventSystem, Text: "[post-turn] committed abc1234: fix: the thing"},
		ai.Event{Kind: ai.EventText, Text: "next turn"},
	)
	if len(msgs) != 3 {
		t.Fatalf("want assistant/system/assistant frames, got %d: %+v", len(msgs), msgs)
	}
	notice := msgs[1]
	if notice.Role != "system" {
		t.Errorf("notice role = %q, want system", notice.Role)
	}
	if got := notice.Parts[0].Text; got != "[post-turn] committed abc1234: fix: the thing" {
		t.Errorf("notice text = %q", got)
	}
	if msgs[2].ID == msgs[0].ID {
		t.Error("the turn after a notice reused the earlier text id, so a viewer that dedupes by id would overwrite it")
	}
	if msgs[2].Parts[0].Text != "next turn" {
		t.Errorf("text after a notice = %q, want a fresh buffer", msgs[2].Parts[0].Text)
	}
}

func TestPromptRunAccumulator_CapturesSessionAndUsage(t *testing.T) {
	acc := newPromptEventAccumulator(func(session.Message) {}, fakeTaskSink{}, "m", "b")
	acc.handle(0, ai.Event{Kind: ai.EventSystem, SessionID: "sess-1"})
	acc.handle(0, ai.Event{Kind: ai.EventResult, Usage: &ai.Usage{InputTokens: 10, OutputTokens: 4}, CostUSD: 0.02})
	if acc.sessionID != "sess-1" {
		t.Fatalf("sessionID = %q, want sess-1", acc.sessionID)
	}
	if acc.usage.InputTokens != 10 || acc.usage.OutputTokens != 4 {
		t.Fatalf("usage = %+v, want 10/4", acc.usage)
	}
	if acc.cost != 0.02 {
		t.Fatalf("cost = %v, want 0.02", acc.cost)
	}
}

// TestPromptRunAccumulator_VerdictsGetTheirOwnRole covers the session half of a
// verify verdict: it is the run's outcome, so a stored transcript must let a
// reader select the verdicts by role rather than searching the system lines for
// prose that looks like one.
func TestPromptRunAccumulator_VerdictsGetTheirOwnRole(t *testing.T) {
	msgs := collectEntries("m", "b",
		ai.Event{Kind: ai.EventText, Text: "applying the fix"},
		ai.Event{Kind: ai.EventVerifyFailed, Text: "failed in 5m7s: sh failed — verify:oipa-cli test x.yaml"},
		ai.Event{Kind: ai.EventVerified, Success: true, Text: "passed in 4m2s — verify:oipa-cli test x.yaml"},
	)

	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.Role)
	}
	// The in-flight text is flushed first, so the verdict never lands inside the
	// sentence the model was part-way through.
	want := []string{"assistant", session.RoleVerifyFailed, session.RoleVerified}
	if strings.Join(roles, ",") != strings.Join(want, ",") {
		t.Fatalf("roles = %v, want %v", roles, want)
	}
	last := msgs[len(msgs)-1]
	if last.Parts[0].Text != "passed in 4m2s — verify:oipa-cli test x.yaml" {
		t.Fatalf("verdict parts = %+v, want the report verbatim", last.Parts)
	}
}

// verifyReport builds a report of `done` passing leaves out of `total`, the
// shape a fixture runner streams while it works.
func verifyReport(done, total int) *api.VerifyReport {
	tests := make([]api.VerifyNode, 0, total)
	for i := 0; i < total; i++ {
		node := api.VerifyNode{Name: fmt.Sprintf("check %d", i+1)}
		if i < done {
			node.Passed = true
		} else {
			node.Pending = true
		}
		tests = append(tests, node)
	}
	report := api.VerifyReport{
		Kind: api.VerifyKindFunc, Name: "fixture", Ran: true, Tests: tests,
		Summary: api.SummarizeNodes(tests), State: api.StateForReport(tests),
	}
	report.Passed = report.State == api.VerifyStatePassed
	return &report
}

// A verdict is a structure as much as a sentence: the webapp draws the
// verification tree, and the only place the transcript could carry it was the
// prose. The typed report rides alongside the text as an AI SDK data part.
func TestPromptRunAccumulator_VerdictCarriesTheTypedReport(t *testing.T) {
	report := verifyReport(3, 3)
	msgs := collectEntries("m", "b", ai.Event{
		Kind: ai.EventVerified, Success: true, Text: "passed in 4ms — fixture", Raw: report,
	})

	if len(msgs) != 1 {
		t.Fatalf("want one verdict frame, got %d", len(msgs))
	}
	parts := msgs[0].Parts
	if len(parts) != 2 {
		t.Fatalf("verdict parts = %+v, want the text then the report", parts)
	}
	if parts[0].Type != session.PartText || parts[1].Type != session.PartVerify {
		t.Fatalf("part types = %q/%q, want %q then %q", parts[0].Type, parts[1].Type, session.PartText, session.PartVerify)
	}
	var round api.VerifyReport
	if err := json.Unmarshal(parts[1].Data, &round); err != nil {
		t.Fatalf("data-verify part does not decode as a report: %v", err)
	}
	if err := round.Validate(); err != nil {
		t.Fatalf("round-tripped report is not valid: %v", err)
	}
	if round.Summary != report.Summary || round.State != report.State {
		t.Fatalf("round-tripped report = %+v, want %+v", round, *report)
	}
}

// Progress is stream state, not transcript: it is superseded within the second,
// and the replay buffer every later subscriber receives in full is the wrong
// place for it.
func TestPromptRunAccumulator_ProgressPublishesFramesAndNoEntries(t *testing.T) {
	var frames []VerifyFrame
	var entries []session.Message
	acc := newPromptEventAccumulator(func(m session.Message) { entries = append(entries, m) }, fakeTaskSink{}, "m", "b")
	acc.verify = func(f VerifyFrame) { frames = append(frames, f) }

	for done := 1; done <= 3; done++ {
		acc.handle(0, ai.Event{Kind: ai.EventVerifyProgress, Tool: "fixture", Raw: verifyReport(done, 3)})
	}
	if len(entries) != 0 {
		t.Fatalf("progress produced %d transcript entries, want none: %+v", len(entries), entries)
	}
	if len(frames) != 3 {
		t.Fatalf("progress frames = %d, want one per snapshot", len(frames))
	}
	for i, frame := range frames {
		if frame.Done {
			t.Fatalf("frame %d is marked done while the check is still running", i)
		}
		if frame.Report.Summary.Passed != i+1 {
			t.Fatalf("frame %d passed = %d, want %d", i, frame.Report.Summary.Passed, i+1)
		}
	}

	acc.handle(0, ai.Event{Kind: ai.EventVerified, Success: true, Text: "passed in 4ms — fixture", Raw: verifyReport(3, 3)})
	if len(frames) != 4 || !frames[3].Done {
		t.Fatalf("final frame = %+v, want the verdict marked done", frames[len(frames)-1])
	}
	if len(entries) != 1 {
		t.Fatalf("verdict produced %d entries, want exactly one", len(entries))
	}
}

// A count divided by a total the producer has not finished settling — more
// running leaves reported than the tree has rows — must not read as negative
// progress in the one line a person is watching.
func TestVerifyProgressStatus_ClampsDoneAtZero(t *testing.T) {
	report := api.VerifyReport{Name: "fixture", Summary: api.VerifySummary{Total: 2, Pending: 2, Running: 1}}
	if got := verifyProgressStatus(report); got != "verifying fixture 0/2" {
		t.Fatalf("verifyProgressStatus = %q, want the count clamped at zero", got)
	}
	failing := api.VerifyReport{Name: "fixture", Summary: api.VerifySummary{Total: 2, Running: 3, Failed: 1}}
	if got := verifyProgressStatus(failing); got != "verifying fixture 0/2, 1 failed" {
		t.Fatalf("verifyProgressStatus = %q, want the count clamped at zero", got)
	}
}

// The verdict is what turns the verification panel from "running" to a result.
// A verdict event whose Raw carries no report used to publish nothing at all, so
// the panel sat on the last progress snapshot forever, spinner and all.
func TestPromptRunAccumulator_VerdictWithoutAReportStillFinishesTheFrame(t *testing.T) {
	var frames []VerifyFrame
	acc := newPromptEventAccumulator(func(session.Message) {}, fakeTaskSink{}, "m", "b")
	acc.verify = func(f VerifyFrame) { frames = append(frames, f) }

	snapshot := verifyReport(1, 3)
	acc.handle(0, ai.Event{Kind: ai.EventVerifyProgress, Tool: "fixture", Raw: snapshot})
	acc.handle(0, ai.Event{Kind: ai.EventVerifyFailed, Text: "failed in 4ms — fixture"})

	if len(frames) != 2 {
		t.Fatalf("frames = %d, want the snapshot and then the verdict", len(frames))
	}
	if !frames[1].Done {
		t.Fatalf("verdict frame = %+v, want Done", frames[1])
	}
	if frames[1].Report != snapshot {
		t.Fatalf("verdict frame report = %+v, want the last snapshot kept rather than blanked", frames[1].Report)
	}
}

// A progress snapshot with no report is the one frame that must be dropped:
// publishing it would blank whatever the last real snapshot put on screen.
func TestPromptRunAccumulator_ProgressWithoutAReportPublishesNothing(t *testing.T) {
	var frames []VerifyFrame
	acc := newPromptEventAccumulator(func(session.Message) {}, fakeTaskSink{}, "m", "b")
	acc.verify = func(f VerifyFrame) { frames = append(frames, f) }

	acc.handle(0, ai.Event{Kind: ai.EventVerifyProgress, Tool: "fixture"})
	if len(frames) != 0 {
		t.Fatalf("frames = %+v, want none", frames)
	}
}

// Every other publisher on the stream stops at done; setVerify did not, so a
// check reporting after the run's terminal frame appended to a buffer whose
// subscribers were already closed.
func TestRunStreamSetVerify_StopsAtDone(t *testing.T) {
	stream := newRunStream()
	stream.fail("stopped")
	stream.setVerify(VerifyFrame{Report: verifyReport(1, 1), Done: true})

	snapshot, ch := stream.subscribeEvents()
	stream.unsubscribeEvents(ch)
	if snapshot.Verify != nil {
		t.Fatalf("verify frame = %+v, want nothing published after the run ended", snapshot.Verify)
	}
}
