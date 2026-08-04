package cli

import (
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/ai/agent/verify"
)

// verifyPassed reports whether the last verify verdict passed. With no verify
// hooks, the run passed. The last-verdict read is sound because the runner
// stops each verification round at its first failure (see agent.verifyPassed),
// so the list's final entry is always the final round's outcome.
func verifyPassed(verdicts []agent.VerifyResult) bool {
	if len(verdicts) == 0 {
		return true
	}
	return verdicts[len(verdicts)-1].Valid
}

// verifyReason surfaces the last failing verifier's reason in a run summary.
func verifyReason(verdicts []agent.VerifyResult) string {
	if len(verdicts) == 0 {
		return ""
	}
	last := verdicts[len(verdicts)-1]
	if last.Valid {
		return ""
	}
	if vd, ok := last.Output.(verify.Verdict); ok && vd.Reason != "" {
		return vd.Reason
	}
	return "verification failed"
}
