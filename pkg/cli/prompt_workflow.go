package cli

import "github.com/flanksource/captain/pkg/ai/agent"

// verdictReason surfaces the last (failing) verifier reason in a run summary.
func verdictReason(verdicts []agent.Verdict) string {
	if len(verdicts) == 0 {
		return ""
	}
	return verdicts[len(verdicts)-1].Reason
}
