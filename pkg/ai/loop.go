package ai

import (
	"context"
	"fmt"
)

// LoopOptions configures a RunUntil run. The driver is provider-agnostic but
// only useful with a StreamingProvider — buffered providers can be wrapped in
// a synthetic streamer if needed by the caller.
type LoopOptions struct {
	Provider      StreamingProvider
	MaxIterations int     // 0 → 3
	MaxCostUSD    float64 // 0 → unbounded
	SessionReuse  bool    // forward prev SessionID into next Request

	// BuildRequest is called before every iteration. Iter starts at 0; prev
	// is nil on the first call. Returning (req, false) stops the loop with
	// reason "condition-met" — use this to short-circuit when the caller's
	// success criterion is satisfied (e.g. zero lint violations).
	BuildRequest func(iter int, prev *LoopIteration) (Request, bool)

	// OnEvent (optional) is invoked for every Event the provider emits, in
	// arrival order, alongside the iteration index. Use it to render live
	// progress. Events are also accumulated into LoopIteration.Events.
	OnEvent func(iter int, ev Event)
}

// LoopIteration captures a single Provider.ExecuteStream invocation.
type LoopIteration struct {
	Iteration int
	Request   Request
	Events    []Event
	SessionID string
	CostUSD   float64
	Usage     Usage
	Success   bool
	Err       error
}

// LoopResult bundles the entire run.
type LoopResult struct {
	Iterations []*LoopIteration
	TotalCost  float64
	StopReason string // "condition-met" | "max-iterations" | "max-cost" | "error"
}

const defaultLoopMaxIterations = 3

// RunUntil drives Provider.ExecuteStream in a loop. It stops when:
//   - BuildRequest returns (_, false)         → reason "condition-met"
//   - len(Iterations) reaches MaxIterations   → reason "max-iterations"
//   - cumulative cost ≥ MaxCostUSD pre-check  → reason "max-cost"
//   - an iteration returns a non-nil error    → reason "error"
//
// MaxCostUSD is enforced before each new iteration starts, so the total never
// exceeds the budget on the strength of a single overshooting call.
func RunUntil(ctx context.Context, opts LoopOptions) (*LoopResult, error) {
	if opts.Provider == nil {
		return nil, fmt.Errorf("RunUntil: Provider is required")
	}
	if opts.BuildRequest == nil {
		return nil, fmt.Errorf("RunUntil: BuildRequest is required")
	}
	maxIter := opts.MaxIterations
	if maxIter <= 0 {
		maxIter = defaultLoopMaxIterations
	}

	result := &LoopResult{}

	for {
		var prev *LoopIteration
		if n := len(result.Iterations); n > 0 {
			prev = result.Iterations[n-1]
		}

		req, cont := opts.BuildRequest(len(result.Iterations), prev)
		if !cont {
			result.StopReason = "condition-met"
			return result, nil
		}
		if len(result.Iterations) >= maxIter {
			result.StopReason = "max-iterations"
			return result, nil
		}
		if opts.MaxCostUSD > 0 && result.TotalCost >= opts.MaxCostUSD {
			result.StopReason = "max-cost"
			return result, nil
		}
		if opts.SessionReuse && prev != nil && prev.SessionID != "" && req.SessionID == "" {
			req.SessionID = prev.SessionID
		}

		iter := &LoopIteration{
			Iteration: len(result.Iterations),
			Request:   req,
		}
		runOneIteration(ctx, opts, req, iter)
		result.Iterations = append(result.Iterations, iter)
		result.TotalCost += iter.CostUSD

		if iter.Err != nil {
			result.StopReason = "error"
			return result, iter.Err
		}
	}
}

func runOneIteration(ctx context.Context, opts LoopOptions, req Request, iter *LoopIteration) {
	events, err := opts.Provider.ExecuteStream(ctx, req)
	if err != nil {
		iter.Err = err
		return
	}

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return
			}
			iter.Events = append(iter.Events, ev)
			if opts.OnEvent != nil {
				opts.OnEvent(iter.Iteration, ev)
			}
			switch ev.Kind {
			case EventResult:
				iter.Success = ev.Success
				iter.CostUSD = ev.CostUSD
				if ev.Usage != nil {
					iter.Usage = *ev.Usage
				}
				// Providers that report no cost (codex-cli/cmux, gemini-cli) leave
				// CostUSD at 0, which would make budget enforcement and rollups treat
				// the run as free (finding C2). Price the usage from the registry so
				// MaxCostUSD sees a real number for every backend.
				if iter.CostUSD == 0 && ev.Usage != nil {
					backend := opts.Provider.GetBackend()
					model := ev.Model
					if model == "" {
						model = opts.Provider.GetModel()
					}
					iter.CostUSD = PriceResponse(backend, model, &Response{Backend: backend, Model: model, Usage: *ev.Usage}).Total()
				}
				if ev.Error != "" && iter.Err == nil {
					iter.Err = fmt.Errorf("claude returned: %s", ev.Error)
				}
			case EventSystem:
				if ev.SessionID != "" {
					iter.SessionID = ev.SessionID
				}
			case EventError:
				if iter.Err == nil {
					iter.Err = fmt.Errorf("%s", ev.Error)
				}
			}
		case <-ctx.Done():
			if iter.Err == nil {
				iter.Err = ctx.Err()
			}
			return
		}
	}
}
