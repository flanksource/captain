package promptrun

import (
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
)

// Result is one run's outcome.
type Result struct {
	// Response is the runner's accumulated response: final text, structured
	// data, terminal outcome, and the Workspace (cwd, changed files, notices).
	Response *ai.Response
	// Verdicts is every verify verdict in the order it was reached; Report is
	// the last one that carries a report — the run's answer.
	Verdicts []agent.VerifyResult
	Report   *api.VerifyReport
	// Passed is Passed(Verdicts): the final round's verdict, or true when the
	// run declared nothing to verify.
	Passed bool
	// Loop is the generate loop's own record — nil for a verify-only run.
	Loop *ai.LoopResult
	// StructuredData and TerminalOutcome are lifted from Response for callers
	// that read only the answer.
	StructuredData  any
	TerminalOutcome *api.TerminalOutcome
	// SessionID is the provider's session for this run; Model the one that
	// answered; Usage and CostUSD the whole run's, summed across iterations.
	SessionID string
	Model     string
	Usage     api.Usage
	CostUSD   float64
	Duration  time.Duration
}

// Passed reports whether the run's last verify verdict passed, or trivially
// true when no Verify hooks ran. It is agent.VerifyPassed — the rule the runner
// itself sets HookContext.Verified from — rather than a second copy of it, so a
// caller reading a Result and a Post hook reading its context can never disagree
// about whether the run verified.
func Passed(verdicts []agent.VerifyResult) bool {
	return agent.VerifyPassed(verdicts)
}

// FailureReason is the last failing verdict's reason, for a run summary; empty
// when the run passed.
func FailureReason(verdicts []agent.VerifyResult) string {
	if len(verdicts) == 0 {
		return ""
	}
	last := verdicts[len(verdicts)-1]
	if last.Valid {
		return ""
	}
	if last.Report != nil && last.Report.Reason != "" {
		return last.Report.Reason
	}
	return "verification failed"
}

// FinalReport is the run's verdict: everything the last round judged, as one
// report.
//
// A round runs every verifier the workflow declares, so it produces one report
// per verifier. Taking the last of them threw the rest away — a round of
// `commands` + `fixture` came back as the fixture's tree alone, and the run's
// summary counted half of what had actually run. A round of one report is that
// report, unwrapped; a round of several is api.MergeReports, which nests each
// under its own group node. A verdict carrying no report contributes nothing
// rather than blanking the round.
func FinalReport(verdicts []agent.VerifyResult) (*api.VerifyReport, error) {
	reports := lastRoundReports(verdicts)
	switch len(reports) {
	case 0:
		return nil, nil
	case 1:
		return reports[0], nil
	}
	round := make([]api.VerifyReport, 0, len(reports))
	for _, r := range reports {
		round = append(round, *r)
	}
	merged, err := api.MergeReports(RoundName, round...)
	if err != nil {
		return nil, err
	}
	return &merged, nil
}

// RoundName names a merged round wherever one is built, so the row a host
// persists and the report the webapp renders agree on what to call it.
const RoundName = "verify"

// lastRoundReports is every report the highest-numbered round produced, in the
// order the verifiers voted. Rounds are identified by the turn they judged,
// which each verdict already carries.
func lastRoundReports(verdicts []agent.VerifyResult) []*api.VerifyReport {
	last, found := 0, false
	for _, v := range verdicts {
		if v.Report != nil && (!found || v.Iteration > last) {
			last, found = v.Iteration, true
		}
	}
	if !found {
		return nil
	}
	var reports []*api.VerifyReport
	for _, v := range verdicts {
		if v.Report != nil && v.Iteration == last {
			reports = append(reports, v.Report)
		}
	}
	return reports
}

// runIdentity is what the event stream says about the run that the runner's
// response does not carry: the model that answered and the session it ran in.
type runIdentity struct {
	sessionID string
	model     string
}

func (r *runIdentity) observe(ev ai.Event) {
	if ev.Model != "" {
		r.model = ev.Model
	}
	if ev.Kind == ai.EventSystem && ev.SessionID != "" {
		r.sessionID = ev.SessionID
	}
}

func newResult(out agent.Result[string], identity runIdentity, duration time.Duration) (Result, error) {
	report, err := FinalReport(out.Verdicts)
	result := Result{
		Response: out.Response,
		Verdicts: out.Verdicts,
		Report:   report,
		Passed:   Passed(out.Verdicts),
		Loop:     out.Loop,
		Model:    identity.model,
		Duration: duration,
	}
	if out.Response != nil {
		result.StructuredData = out.Response.StructuredData
		result.TerminalOutcome = out.Response.TerminalOutcome
		if out.Response.Workspace != nil {
			result.SessionID = out.Response.Workspace.SessionID
		}
		if result.Model == "" {
			result.Model = out.Response.Model
		}
	}
	if result.SessionID == "" {
		result.SessionID = identity.sessionID
	}
	if out.Loop != nil {
		result.CostUSD = out.Loop.TotalCost
		for _, turn := range out.Loop.Iterations {
			if result.SessionID == "" {
				result.SessionID = turn.SessionID
			}
			result.Usage = addUsage(result.Usage, turn.Usage)
		}
	}
	return result, err
}

func addUsage(into, from api.Usage) api.Usage {
	into.InputTokens += from.InputTokens
	into.OutputTokens += from.OutputTokens
	into.ReasoningTokens += from.ReasoningTokens
	into.CacheReadTokens += from.CacheReadTokens
	into.CacheWriteTokens += from.CacheWriteTokens
	return into
}
