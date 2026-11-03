// Specs for a run whose resolved sandbox executes remotely: promptrun relocates
// it through the adapter's RemoteExecutor instead of running the agent here.
package promptrun_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/promptrun"
	"github.com/flanksource/captain/pkg/sandbox/adapter"
)

// relocatingSandbox stands in for a remote-exec adapter: it records the run it
// was handed and answers it, so a spec can tell a relocated run from one that
// ran here.
type relocatingSandbox struct {
	prepared int
	closed   int
	executed []api.Spec
}

func (s *relocatingSandbox) Kind() api.SandboxKind { return api.SandboxGitAgent }
func (s *relocatingSandbox) Prepare(context.Context, *api.Spec) (*api.SandboxSession, error) {
	s.prepared++
	return &api.SandboxSession{}, nil
}
func (s *relocatingSandbox) Close() error              { s.closed++; return nil }
func (s *relocatingSandbox) IsolatesWorkspace() bool   { return true }
func (s *relocatingSandbox) ProvidesEgressProxy() bool { return true }
func (s *relocatingSandbox) Execute(_ context.Context, spec api.Spec) (*api.Response, error) {
	s.executed = append(s.executed, spec)
	return &api.Response{Text: "done remotely", Model: "sonnet"}, nil
}

var _ = Describe("promptrun.Run with a remote-executing sandbox", func() {
	var sandbox *relocatingSandbox
	BeforeEach(func() {
		sandbox = &relocatingSandbox{}
		api.RegisterSandbox(api.SandboxGitAgent, func(api.SandboxConfig) (api.Sandbox, error) { return sandbox, nil })
		DeferCleanup(func() { api.RegisterSandbox(api.SandboxGitAgent, adapter.GitAgent) })
		// A run that falls through to local execution must fail here rather than
		// start a real agent CLI on this machine.
		GinkgoT().Setenv("PATH", GinkgoT().TempDir())
	})

	It("relocates an embedded host's run instead of running the agent here", func() {
		result, err := promptrun.Run(context.Background(), promptrun.Input{
			Resolved: api.ResolvedSpec{Spec: api.Spec{
				Model:  api.Model{Name: "sonnet", Provider: api.Anthropic, Mode: api.ModeCLI},
				Prompt: api.Prompt{User: "review"},
			}},
			Config:  ai.Config{SandboxSelection: &api.SandboxConfig{Kind: api.SandboxGitAgent, Name: "review-pool"}},
			Timeout: time.Minute,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Response.Text).To(Equal("done remotely"))
		Expect(sandbox.executed).To(HaveLen(1))
		Expect(sandbox.executed[0].Prompt.User).To(Equal("review"))
		Expect(sandbox.prepared).To(Equal(1))
		Expect(sandbox.closed).To(Equal(1))
	})
})
