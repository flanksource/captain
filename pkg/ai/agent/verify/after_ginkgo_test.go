package verify

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("workflow.after", func() {
	var (
		cwd      string
		streamed []ai.Event
	)

	BeforeEach(func() {
		cwd = GinkgoT().TempDir()
		streamed = nil
	})

	run := func(provider ai.StreamingProvider, wf *api.Workflow, extra ...any) (agent.Result[string], error) {
		checks, err := HooksFor(context.Background(), wf, Options{})
		Expect(err).NotTo(HaveOccurred())
		after, err := AfterHooksFor(wf, Options{})
		Expect(err).NotTo(HaveOccurred())
		hooks := append(append(checks, extra...), after...)
		runner := &agent.Runner[string]{
			Provider: provider, Cwd: cwd, MaxIterations: 1, Hooks: hooks,
			Request: ai.Request{Prompt: api.Prompt{User: "fix it"}, Workflow: wf},
			OnEvent: func(_ int, ev ai.Event) {
				if ev.Kind == api.EventVerified || ev.Kind == api.EventVerifyFailed {
					streamed = append(streamed, ev)
				}
			},
		}
		return runner.Run(context.Background())
	}
	ran := func(marker string) bool {
		_, err := os.Stat(filepath.Join(cwd, marker))
		return err == nil
	}
	tools := func() []string {
		var names []string
		for _, ev := range streamed {
			names = append(names, ev.Tool)
		}
		return names
	}

	It("runs after verification passed and reports each command on the run's stream", func() {
		_, err := run(&silentProvider{}, &api.Workflow{
			Verify: &api.Verify{Commands: []string{"true"}},
			After:  []api.AfterStep{{Commands: []string{"touch landed"}}},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(ran("landed")).To(BeTrue())
		Expect(tools()).To(Equal([]string{"verify:true", "after:touch landed"}))
		Expect(streamed[1].Kind).To(Equal(api.EventVerified))
	})

	It("is skipped when verification failed", func() {
		_, err := run(&silentProvider{}, &api.Workflow{
			Verify: &api.Verify{Commands: []string{"false"}},
			After:  []api.AfterStep{{Commands: []string{"touch landed"}}},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(ran("landed")).To(BeFalse())
		Expect(tools()).To(Equal([]string{"verify:false"}))
	})

	It("treats onVerify as onSuccess when the workflow declares no verification", func() {
		_, err := run(&silentProvider{}, &api.Workflow{After: []api.AfterStep{{Commands: []string{"touch landed"}}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(ran("landed")).To(BeTrue())
	})

	It("fails the run at the first failing command, naming it and its output", func() {
		_, err := run(&silentProvider{}, &api.Workflow{After: []api.AfterStep{{
			Commands: []string{"touch first", "echo merge conflict in main.go; exit 3", "touch never"},
		}}})
		Expect(err).To(MatchError(And(
			ContainSubstring("echo merge conflict in main.go; exit 3"),
			ContainSubstring("merge conflict in main.go"),
		)))
		Expect(ran("first")).To(BeTrue())
		Expect(ran("never")).To(BeFalse(), "commands stop at the first failure")
		Expect(streamed[len(streamed)-1].Kind).To(Equal(api.EventVerifyFailed))
	})

	It("runs an always step after the run itself failed, and skips onSuccess", func() {
		_, err := run(&failingProvider{}, &api.Workflow{After: []api.AfterStep{
			{When: api.CommitWhenSuccess, Commands: []string{"touch landed"}},
			{When: api.CommitWhenAlways, Commands: []string{"touch cleaned"}},
		}})
		Expect(err).To(MatchError(ContainSubstring("provider exploded")))
		Expect(ran("landed")).To(BeFalse())
		Expect(ran("cleaned")).To(BeTrue())
	})

	It("does not land when an earlier post hook of the run failed", func() {
		_, err := run(&silentProvider{}, &api.Workflow{After: []api.AfterStep{{Commands: []string{"touch landed"}}}},
			&failingPost{})
		Expect(err).To(MatchError(ContainSubstring("commit refused")))
		Expect(ran("landed")).To(BeFalse())
	})

	It("resolves a relative cwd against the run's working directory", func() {
		Expect(os.Mkdir(filepath.Join(cwd, "sub"), 0o755)).To(Succeed())
		_, err := run(&silentProvider{}, &api.Workflow{After: []api.AfterStep{{Cwd: "sub", Commands: []string{"touch here"}}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(ran(filepath.Join("sub", "here"))).To(BeTrue())
	})

	It("refuses an invalid declaration before anything runs", func() {
		_, err := AfterHooksFor(&api.Workflow{After: []api.AfterStep{{When: "later", Commands: []string{"true"}}}}, Options{})
		Expect(err).To(MatchError(ContainSubstring("workflow.after[0].when")))
	})
})

// failingProvider errors on every turn, so the run itself fails.
type failingProvider struct{ silentProvider }

func (*failingProvider) ExecuteStream(context.Context, ai.Request) (<-chan ai.Event, error) {
	return nil, errors.New("provider exploded")
}

// failingPost stands in for a commit hook that refused at the run phase.
type failingPost struct{}

func (*failingPost) Name() string                               { return "commit:run" }
func (*failingPost) Phases() []agent.Phase                      { return []agent.Phase{agent.PhaseRun} }
func (*failingPost) Post(*agent.HookContext, agent.Phase) error { return errors.New("commit refused") }
