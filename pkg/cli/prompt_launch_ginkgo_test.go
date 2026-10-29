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
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/clicky/task"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

// launchFakeProvider answers only once release is closed, so a spec can observe
// the run while it is still in flight.
type launchFakeProvider struct {
	release <-chan struct{}
	output  string
	// turns are streamed, in order, once release is closed and before the result.
	turns []ai.Event
}

func (p launchFakeProvider) GetModel() string       { return "claude-sonnet-5" }
func (p launchFakeProvider) GetRuntime() ai.Runtime { return ai.RuntimeOf(ai.Anthropic, ai.ModeAPI) }

func (p launchFakeProvider) Execute(context.Context, ai.Request) (*ai.Response, error) {
	return nil, errors.New("launchFakeProvider only streams")
}

func (p launchFakeProvider) ExecuteStream(ctx context.Context, _ ai.Request) (<-chan ai.Event, error) {
	events := make(chan ai.Event, len(p.turns)+1)
	go func() {
		defer close(events)
		select {
		case <-p.release:
			for _, turn := range p.turns {
				events <- turn
			}
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
		turns    []ai.Event
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
		GinkgoT().Setenv("HOME", filepath.Join(dir, "home"))
		release, configs, turns = make(chan struct{}), make(chan ai.Config, 4), nil
		runtime := ai.RuntimeOf(ai.Anthropic, ai.ModeAPI)
		ai.RegisterProvider(runtime, func(cfg ai.Config) (ai.Provider, error) {
			configs <- cfg
			return launchFakeProvider{release: release, output: output, turns: turns}, nil
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

	It("renders a Jev prompt with caller and inline tools, recording the inline tool as ignored by jev", func() {
		path := writePrompt("---\nmodel: api:jev-latest\ntools:\n  emit-choices:\n    exec: echo {}\n    readonly: true\noutput:\n  schemaType: typesafe-schema\n  schema:\n    valid:\n      type: noul\n      instructions: Is this valid?\n---\nReview the change")

		rendered, err := RenderPrompt(ctx, PromptInvocation{Reference: path, Stdin: strings.NewReader(""), Tools: []api.ToolDefinition{reporter}})

		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.Resolution.Provenance["/tools/emit-choices"].Ignored).To(PointTo(MatchFields(IgnoreExtras, Fields{
			"By": MatchFields(IgnoreExtras, Fields{"Kind": Equal(api.FieldSourceCatalog), "Name": Equal("jev")}),
		})))
		encoded, err := json.Marshal(rendered)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(encoded)).To(ContainSubstring(`"ignored":{"by":{"kind":"catalog","name":"jev"`))
	})

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

	Describe("placing the run's task", func() {
		const callerTaskName = "messaging-review:report.mdx"

		launchInto := func(ctx context.Context, group *task.Group) PromptRunResult {
			GinkgoHelper()
			path := writePrompt("---\nmodel: api:sonnet\n---\nReview the change")
			started, err := LaunchPrompt(ctx, PromptInvocation{
				Reference: path, Stdin: strings.NewReader(""), Group: group, Name: callerTaskName,
			})
			Expect(err).NotTo(HaveOccurred())
			Eventually(configs).Should(Receive(), "the provider is built once the run is admitted")
			return started
		}

		runFrameSessionID := func(runID string) string {
			GinkgoHelper()
			stream, ok := promptRuns.get(runID)
			Expect(ok).To(BeTrue())
			snapshot, ch := stream.subscribeEvents()
			stream.unsubscribeEvents(ch)
			Expect(snapshot.Done).To(BeFalse())
			return snapshot.Run.SessionID
		}

		onlyTask := func(group task.TypedGroup[PromptRunSummary]) *task.Task {
			GinkgoHelper()
			tasks := group.GetTasks()
			Expect(tasks).To(HaveLen(1), "the run adds exactly one task to the caller's group")
			return tasks[0].GetTask()
		}

		It("adds one task named by the caller to its group and logs the admitted session before the run completes", func(ctx SpecContext) {
			openCLIRecordDB()
			group := task.StartGroup[PromptRunSummary]("lint prompt")

			started := launchInto(ctx, group.Group)

			Expect(task.SnapshotByID(started.RunID)).To(BeEmpty(), "the run starts no prompt group of its own")
			sessionID := runFrameSessionID(started.RunID)
			Expect(uuid.Parse(sessionID)).Error().NotTo(HaveOccurred(), "the first run frame names the captain session")
			run := onlyTask(group)
			Expect(run.Name()).To(Equal(callerTaskName))
			Expect(task.SnapshotTask(run, group.Group).Logs).To(ContainElement(HaveField("Message", "session "+sessionID)))

			close(release)
			summary, err := AwaitPromptRun(ctx, started.RunID)
			Expect(err).NotTo(HaveOccurred())
			Expect(summary).To(SatisfyAll(HaveField("Success", true), HaveField("SessionID", sessionID)))
		})

		It("announces a session that session get reads back with the API run's transcript, tool calls and token usage", func(ctx SpecContext) {
			openCLIRecordDB()
			lookup := api.Part{Type: api.PartToolRequest, ToolRequest: &api.ToolRequest{ToolCallID: "toolu_report", Name: reporter.Name, Input: json.RawMessage(`{"line":3}`)}}
			turns = []ai.Event{
				{Kind: ai.EventTurn, Message: &api.Message{Role: api.RoleUser, Parts: []api.Part{{Type: api.PartText, Text: "Review the change"}}}},
				{Kind: ai.EventTurn, Usage: &ai.Usage{InputTokens: 2000, OutputTokens: 500}, Message: &api.Message{Role: api.RoleAssistant, Parts: []api.Part{lookup}}},
				{Kind: ai.EventToolUse, Tool: reporter.Name, ToolCallID: "toolu_report", Input: map[string]any{"line": 3}},
				{Kind: ai.EventToolResult, Tool: reporter.Name, ToolCallID: "toolu_report", Success: true, Text: "recorded"},
				{Kind: ai.EventTurn, Message: &api.Message{Role: api.RoleTool, Parts: []api.Part{{Type: api.PartToolResult, ToolResult: &api.ToolResult{ToolCallID: "toolu_report", Output: json.RawMessage(`"recorded"`)}}}}},
				{Kind: ai.EventTurn, Usage: &ai.Usage{InputTokens: 3000, OutputTokens: 700}, Message: &api.Message{Role: api.RoleAssistant, Parts: []api.Part{{Type: api.PartText, Text: output}}}},
			}
			started := launchInto(ctx, task.StartGroup[PromptRunSummary]("lint prompt").Group)
			sessionID := runFrameSessionID(started.RunID)
			close(release)
			Expect(AwaitPromptRun(ctx, started.RunID)).Error().NotTo(HaveOccurred())

			got, err := RunSessionGet(ctx, SessionGetOptions{ID: sessionID, Primary: true})

			Expect(err).NotTo(HaveOccurred())
			Expect(got.Sessions).To(HaveLen(1))
			item := got.Sessions[0]
			Expect(item.CaptainID).To(Equal(sessionID))
			Expect(item.Summary.Tokens).To(PointTo(SatisfyAll(HaveField("InputTokens", 5000), HaveField("OutputTokens", 1200))))
			Expect(item.Summary.ToolCalls).To(Equal(1))
			Expect(item.Detail).NotTo(BeNil())
			var parts []session.Part
			for _, message := range item.Detail.Messages {
				parts = append(parts, message.Parts...)
			}
			Expect(parts).To(ContainElement(And(HaveField("ToolName", reporter.Name), HaveField("ToolCallID", "toolu_report"))))
		})

		It("runs unrecorded without a store: no session line, and the run still succeeds", func(ctx SpecContext) {
			group := task.StartGroup[PromptRunSummary]("lint prompt")

			started := launchInto(ctx, group.Group)
			close(release)
			summary, err := AwaitPromptRun(ctx, started.RunID)

			Expect(err).NotTo(HaveOccurred())
			Expect(summary).To(SatisfyAll(HaveField("Success", true), HaveField("SessionID", BeEmpty())))
			Expect(task.SnapshotTask(onlyTask(group), group.Group).Logs).NotTo(ContainElement(HaveField("Message", HavePrefix("session "))))
		})

		It("starts its own kind=prompt run group when the caller names none", func(ctx SpecContext) {
			started := launchInto(ctx, nil)

			snapshots := task.SnapshotByID(started.RunID)
			Expect(snapshots).NotTo(BeEmpty())
			Expect(snapshots[0]).To(SatisfyAll(
				HaveField("Type", "group"),
				HaveField("Name", "prompt review"),
				HaveField("Kind", "prompt"),
				HaveField("Labels", HaveKeyWithValue("prompt", "review")),
			))
			Expect(snapshots[1:]).To(ConsistOf(HaveField("Name", "execute")))
			close(release)
			Expect(AwaitPromptRun(ctx, started.RunID)).Error().NotTo(HaveOccurred())
		})
	})

	It("refuses to await a run the broker does not know", func() {
		_, err := AwaitPromptRun(ctx, "missing-run")

		Expect(err).To(MatchError(ContainSubstring(`unknown prompt run "missing-run"`)))
	})
})
