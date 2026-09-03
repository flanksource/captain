package cli

import (
	"errors"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// verdictReport is the report one turn's verifier produced, stamped for that
// 1-based turn exactly as verify.Plugin stamps it.
func verdictReport(iteration int, passed bool) *api.VerifyReport {
	node := api.VerifyNode{Name: "go test ./...", Passed: passed, Failed: !passed}
	report := api.NewNodeReport(api.VerifyKindCmd, "verify:go test ./...", node)
	report.Iteration = iteration
	return &report
}

func loopWith(turns int, base time.Time, err error) *ai.LoopResult {
	loop := &ai.LoopResult{StopReason: "condition-met"}
	for i := 0; i < turns; i++ {
		started := base.Add(time.Duration(i) * time.Minute)
		iteration := &ai.LoopIteration{
			Iteration:  i,
			Request:    ai.Request{Prompt: api.Prompt{User: "attempt " + string(rune('A'+i))}},
			StartedAt:  started,
			FinishedAt: started.Add(30 * time.Second),
			Success:    true,
		}
		if i == turns-1 {
			iteration.Err = err
		}
		loop.Iterations = append(loop.Iterations, iteration)
	}
	return loop
}

func TestPromptRunIterationRecords_FailingThenPassingTurn(t *testing.T) {
	base := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	loop := loopWith(2, base, nil)
	verdicts := []agent.VerifyResult{
		{Valid: false, Iteration: 1, Report: verdictReport(1, false), Retry: &ai.Request{}},
		{Valid: true, Iteration: 2, Report: verdictReport(2, true)},
	}
	verdicts[0].Report.Feedback = "TestFoo failed"

	records := promptRunIterationRecords(loop, verdicts, false)
	require.Len(t, records, 2)

	assert.Equal(t, 1, records[0].Iteration)
	assert.Equal(t, database.PromptRunIterationStateFailed, records[0].State)
	assert.Equal(t, "TestFoo failed", records[0].Feedback)
	assert.Equal(t, verdicts[0].Report, records[0].VerificationResult)
	assert.Equal(t, map[string]any{"prompt": "attempt A"}, records[0].Request)
	require.NotNil(t, records[0].StartedAt)
	require.NotNil(t, records[0].FinishedAt)
	assert.Equal(t, base, *records[0].StartedAt)
	assert.Equal(t, base.Add(30*time.Second), *records[0].FinishedAt)

	assert.Equal(t, 2, records[1].Iteration)
	assert.Equal(t, database.PromptRunIterationStateSucceeded, records[1].State)
	assert.Empty(t, records[1].Feedback)
	assert.Equal(t, verdicts[1].Report, records[1].VerificationResult)
}

// A turn the provider could not complete is a failure of the turn, not a
// verdict: nothing judged it, and recording it as succeeded would make a
// crashed run read as a clean one.
func TestPromptRunIterationRecords_ProviderErrorIsAFailedTurn(t *testing.T) {
	base := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	records := promptRunIterationRecords(loopWith(1, base, errors.New("upstream 529")), nil, false)

	require.Len(t, records, 1)
	assert.Equal(t, database.PromptRunIterationStateFailed, records[0].State)
	assert.Equal(t, "upstream 529", records[0].Error)
	assert.Nil(t, records[0].VerificationResult)
}

// A stopped run's last turn was interrupted, not judged; "cancelled" is the
// only state that says so.
func TestPromptRunIterationRecords_StoppedRunCancelsTheLastTurn(t *testing.T) {
	base := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	loop := loopWith(2, base, nil)
	verdicts := []agent.VerifyResult{{Valid: false, Iteration: 1, Report: verdictReport(1, false)}}

	records := promptRunIterationRecords(loop, verdicts, true)

	require.Len(t, records, 2)
	assert.Equal(t, database.PromptRunIterationStateFailed, records[0].State)
	assert.Equal(t, database.PromptRunIterationStateCancelled, records[1].State)
}

// A round runs every verifier the workflow declares — `commands` and `fixture`
// both vote on the same turn. Keeping the last verdict per iteration stored the
// fixture's tree and threw the command's away, so the row showed half of what
// the turn was actually judged on.
func TestPromptRunIterationRecords_RollsAWholeRoundIntoOneReport(t *testing.T) {
	base := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	cmd := api.NewNodeReport(api.VerifyKindCmd, "verify:go test ./...", api.VerifyNode{Name: "go test ./...", Passed: true})
	cmd.Ran, cmd.Iteration = true, 1
	fixture := api.NewNodeReport(api.VerifyKindFixture, "acceptance", api.VerifyNode{Name: "TestFoo", Failed: true})
	fixture.Ran, fixture.Iteration, fixture.Feedback = true, 1, "TestFoo: want 3, got 4"

	records := promptRunIterationRecords(loopWith(1, base, nil), []agent.VerifyResult{
		{Valid: true, Iteration: 1, Report: &cmd},
		{Valid: false, Iteration: 1, Report: &fixture},
	}, false)

	require.Len(t, records, 1)
	assert.Equal(t, database.PromptRunIterationStateFailed, records[0].State)
	assert.Equal(t, "TestFoo: want 3, got 4", records[0].Feedback)
	report := records[0].VerificationResult
	require.NotNil(t, report)
	require.Len(t, report.Tests, 2, "both verifiers keep their own group node")
	assert.Equal(t, "verify:go test ./...", report.Tests[0].Name)
	assert.Equal(t, "acceptance", report.Tests[1].Name)
	assert.Equal(t, api.VerifySummary{Total: 2, Passed: 1, Failed: 1}, report.Summary)
	assert.False(t, report.Passed)
	require.NoError(t, report.Validate())
}

// A single-verdict round is that verdict's report, unwrapped: nesting a lone
// check under a group node would change every stored row for no gain.
func TestPromptRunIterationRecords_SingleVerdictIsStoredAsIs(t *testing.T) {
	base := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	verdicts := []agent.VerifyResult{{Valid: true, Iteration: 1, Report: verdictReport(1, true)}}

	records := promptRunIterationRecords(loopWith(1, base, nil), verdicts, false)

	require.Len(t, records, 1)
	assert.Same(t, verdicts[0].Report, records[0].VerificationResult)
}

// A run with no Verify hooks at all has nothing to fail: every completed turn
// stands on its own.
func TestPromptRunIterationRecords_UnverifiedTurnsSucceed(t *testing.T) {
	base := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	records := promptRunIterationRecords(loopWith(1, base, nil), nil, false)

	require.Len(t, records, 1)
	assert.Equal(t, database.PromptRunIterationStateSucceeded, records[0].State)
	assert.Nil(t, records[0].VerificationResult)
}
