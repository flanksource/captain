package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/provider/genkit"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// launchFakeProvider answers only once release is closed, so a spec can observe
// the run while it is still in flight.
type launchFakeProvider struct {
	release <-chan struct{}
	output  string
}

func (p launchFakeProvider) GetModel() string       { return "claude-sonnet-5" }
func (p launchFakeProvider) GetRuntime() ai.Runtime { return ai.RuntimeOf(ai.Anthropic, ai.ModeAPI) }

func (p launchFakeProvider) Execute(context.Context, ai.Request) (*ai.Response, error) {
	return nil, errors.New("launchFakeProvider only streams")
}

func (p launchFakeProvider) ExecuteStream(ctx context.Context, _ ai.Request) (<-chan ai.Event, error) {
	events := make(chan ai.Event, 1)
	go func() {
		defer close(events)
		select {
		case <-p.release:
			events <- ai.Event{Kind: ai.EventResult, Success: true, Model: p.GetModel(), StructuredData: json.RawMessage(p.output)}
		case <-ctx.Done():
		}
	}()
	return events, nil
}

var _ = Describe("Prompt launch", func() {
	var (
		ctx      context.Context
		dir      string
		release  chan struct{}
		configs  chan ai.Config
		verdict  = map[string]any{"verdict": "clean"}
		output   = `{"verdict":"clean"}`
		reporter = api.ToolDefinition{
			Name: "report_violation", Description: "Report one violation",
			DefaultPermission: api.ToolPolicyAllow,
			Handler:           func(context.Context, map[string]any) (any, error) { return "recorded", nil },
		}
	)

	writePrompt := func(content string) string {
		path := filepath.Join(dir, "review.prompt")
		Expect(os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
		return path
	}

	BeforeEach(func() {
		ctx = context.Background()
		dir = GinkgoT().TempDir()
		captainconfig.SetPathForTesting(filepath.Join(dir, "captain.yaml"))
		DeferCleanup(func() { captainconfig.SetPathForTesting("") })
		GinkgoT().Setenv("ANTHROPIC_API_KEY", "test-key")
		release, configs = make(chan struct{}), make(chan ai.Config, 4)
		runtime := ai.RuntimeOf(ai.Anthropic, ai.ModeAPI)
		ai.RegisterProvider(runtime, func(cfg ai.Config) (ai.Provider, error) {
			configs <- cfg
			return launchFakeProvider{release: release, output: output}, nil
		})
		DeferCleanup(func() {
			ai.RegisterProvider(runtime, func(cfg ai.Config) (ai.Provider, error) { return genkit.New(cfg) })
		})
	})

	It("launches a running run that hands the caller tools to the provider and awaits its structured output", func() {
		path := writePrompt("---\nmodel: api:sonnet\noutput:\n  schema:\n    verdict: string\n---\nReview the change")

		started, err := LaunchPrompt(ctx, PromptInvocation{Reference: path, Stdin: strings.NewReader(""), Tools: []api.ToolDefinition{reporter}})
		Expect(err).NotTo(HaveOccurred())
		Expect(started.RunID).NotTo(BeEmpty())

		stream, ok := promptRuns.get(started.RunID)
		Expect(ok).To(BeTrue(), "the launched run is registered with the broker PromptRunHandler serves")
		snapshot, ch := stream.subscribeEvents()
		stream.unsubscribeEvents(ch)
		Expect(snapshot.Done).To(BeFalse())
		Expect(snapshot.Run.Status).To(Equal("running"))

		var cfg ai.Config
		Eventually(configs).Should(Receive(&cfg))
		Expect(cfg.Tools).To(ContainElement(HaveField("Name", reporter.Name)))

		close(release)
		summary, err := AwaitPromptRun(ctx, started.RunID)
		Expect(err).NotTo(HaveOccurred())
		Expect(summary).To(SatisfyAll(
			HaveField("RunID", started.RunID),
			HaveField("Success", true),
			HaveField("StructuredOutput", Equal(verdict)),
		))
	})

	It("carries the caller tools on every runtime variant", func() {
		path := writePrompt("---\nruntimes: [api:sonnet, api:haiku]\n---\nReview the change")

		rendered, err := RenderPrompt(ctx, PromptInvocation{Reference: path, Stdin: strings.NewReader(""), Tools: []api.ToolDefinition{reporter}})

		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.variants).To(HaveLen(2))
		for _, variant := range rendered.variants {
			Expect(variant.Config.Tools).To(ConsistOf(HaveField("Name", reporter.Name)))
		}
	})

	DescribeTable("refuses caller tools on a runtime that cannot expose them",
		func(frontmatter string) {
			path := writePrompt("---\n" + frontmatter + "\n---\nReview the change")

			_, err := RenderPrompt(ctx, PromptInvocation{Reference: path, Stdin: strings.NewReader(""), Tools: []api.ToolDefinition{reporter}})

			Expect(err).To(MatchError(SatisfyAll(ContainSubstring(`"google cli"`), ContainSubstring("cannot expose caller tools"))))
		},
		Entry("the only runtime", "model: cli:gemini-3.5-flash"),
		Entry("one of several runtimes", "runtimes: [api:sonnet, cli:gemini-3.5-flash]"),
	)

	It("stops a launched run, which then ends as stopped and cannot be stopped again", func() {
		path := writePrompt("---\nmodel: api:sonnet\n---\nReview the change")
		started, err := LaunchPrompt(ctx, PromptInvocation{Reference: path, Stdin: strings.NewReader("")})
		Expect(err).NotTo(HaveOccurred())
		Eventually(configs).Should(Receive())

		Expect(StopPromptRun(started.RunID)).To(Succeed())
		summary, err := AwaitPromptRun(ctx, started.RunID)

		Expect(err).To(MatchError(ContainSubstring("stopped")))
		Expect(summary.Success).To(BeFalse())
		Expect(StopPromptRun(started.RunID)).To(MatchError(ErrPromptRunTerminal))
		Expect(StopPromptRun("missing-run")).To(MatchError(ErrUnknownPromptRun))
	})

	It("refuses to await a run the broker does not know", func() {
		_, err := AwaitPromptRun(ctx, "missing-run")

		Expect(err).To(MatchError(ContainSubstring(`unknown prompt run "missing-run"`)))
	})
})
