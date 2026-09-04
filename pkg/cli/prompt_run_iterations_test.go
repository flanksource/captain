package cli

import (
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

// The record derivation itself is specified in pkg/promptrun
// (iterations_ginkgo_test.go); these helpers build the runs the persistence
// specs here file through it.

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
