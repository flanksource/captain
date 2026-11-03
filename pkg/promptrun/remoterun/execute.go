// The environment's side of a relocated run: Execute runs a job's prompt run in the environment's
// checkout, with the host's hooks, and sends the caller each message as it happens.
package remoterun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/ai/agent/setup"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/promptrun"
)

// Serve is how the run in the environment reaches its caller: Send reports a message, Call asks
// for a reply.
type Serve struct {
	Send func(Message) error
	Call func(Message) (Reply, error)
}

// Host is what the host's program in the environment adds to the run: the provider it runs on
// (nil builds it from the job's model, as a local run does), hooks it rebuilt from the job's host
// data, and tools that run in the environment in place of the caller's of the same name.
type Host struct {
	Provider ai.Provider
	Hooks    []any
	Tools    []api.ToolDefinition
}

// callCaller gives in the job's tools, each run by the caller unless local has a tool of its name,
// which runs in the environment instead, and, when the run asks for approvals, an approval
// callback that asks the caller.
func callCaller(in *promptrun.Input, job Job, serve Serve, local []api.ToolDefinition) {
	for _, tool := range job.Tools {
		if i := slices.IndexFunc(local, func(def api.ToolDefinition) bool { return def.Name == tool.Name }); i >= 0 {
			in.Config.Tools = append(in.Config.Tools, local[i])
			continue
		}
		name := tool.Name
		tool.Handler = func(_ context.Context, input map[string]any) (any, error) {
			reply, err := serve.Call(ToolCallMessage(name, input))
			if err == nil && reply.Error != "" {
				err = errors.New(reply.Error)
			}
			if err != nil {
				return nil, fmt.Errorf("tool %s: %w", name, err)
			}
			var output any
			if err := json.Unmarshal(reply.Output, &output); err != nil {
				return nil, fmt.Errorf("decode the output of tool %s: %w", name, err)
			}
			return output, nil
		}
		in.Config.Tools = append(in.Config.Tools, tool)
	}
	if job.Approvals {
		in.Config.OnApproval = func(_ context.Context, request api.ApprovalRequest) (api.ApprovalDecision, error) {
			reply, err := serve.Call(ApprovalMessage(request))
			if err == nil && reply.Error != "" {
				err = errors.New(reply.Error)
			}
			if err == nil && reply.Decision == nil {
				err = errors.New("the caller answered without a decision")
			}
			if err != nil {
				return api.ApprovalDecision{}, fmt.Errorf("approval of %s: %w", request.Tool, err)
			}
			return *reply.Decision, nil
		}
	}
}

// Finished is how a run ended: its result, and its own error.
type Finished struct {
	Result promptrun.Result
	Err    error
}

// Execute runs job in dir, the environment's checkout, sends its start, events and verification
// progress, and returns how it ended, for the caller to report once it has saved what the run
// left behind. Execute returns an error only when a message couldn't be sent, and stops the run
// when its start couldn't.
func Execute(ctx context.Context, job Job, dir string, host Host, serve Serve) (Finished, error) {
	reporter := &reporter{send: serve.Send}
	in := job.Input(dir)
	callCaller(&in, job, serve, host.Tools)
	in.Provider = host.Provider
	in.Hooks = append(append([]any{}, host.Hooks...), &startedHook{reporter: reporter})
	if host.Provider != nil {
		// A supplied provider skips Captain's setup hook, so the run is set up here, as a local
		// run with a supplied provider is.
		in.Hooks = append(in.Hooks, &setup.Plugin{BaseDir: dir})
	}
	in.OnEvent = func(iteration int, event api.Event) {
		reporter.report(EventMessage(iteration, event), "an event")
	}
	in.Verify.Progress = func(report api.VerifyReport) {
		reporter.report(ProgressMessage(report), "verification progress")
	}
	result, err := promptrun.Run(ctx, in)
	if reporter.startErr != nil {
		return Finished{}, reporter.startErr
	}
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	return Finished{Result: result, Err: err}, reporter.err
}

// reporter sends messages one at a time and keeps the first failure.
type reporter struct {
	send     func(Message) error
	mu       sync.Mutex
	err      error
	startErr error
}

func (r *reporter) report(message Message, what string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return
	}
	if err := r.send(message); err != nil {
		r.err = fmt.Errorf("report %s of the run to its caller: %w", what, err)
	}
}

// startedHook reports the run's start. It is the last of the host's hooks, so the spec it reports
// carries what they set on it.
type startedHook struct {
	reporter *reporter
}

func (h *startedHook) Name() string { return "remoterun-started" }

func (h *startedHook) PreRun(hc *agent.HookContext) error {
	var spec api.Spec
	if hc.Request != nil {
		spec = *hc.Request
	}
	h.reporter.mu.Lock()
	defer h.reporter.mu.Unlock()
	if err := h.reporter.send(StartedMessage(spec)); err != nil {
		h.reporter.startErr = fmt.Errorf("report the run's start to its caller: %w", err)
		h.reporter.err = h.reporter.startErr
		return h.reporter.startErr
	}
	return nil
}
