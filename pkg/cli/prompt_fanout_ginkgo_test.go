package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/provider/genkit"
	"github.com/flanksource/captain/pkg/api"
	clickyrpc "github.com/flanksource/clicky/rpc"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("prompt command fan-out", func() {
	var dir string

	BeforeEach(func() {
		GinkgoT().Setenv("CAPTAIN_SESSION_DB_URL", "off")
		dir = GinkgoT().TempDir()
		original := executePromptRequestFunc
		DeferCleanup(func() { executePromptRequestFunc = original })
		executePromptRequestFunc = func(_ context.Context, req ai.Request, cfg ai.Config, _ time.Duration, _ bool) (any, error) {
			return AIPromptResult{Text: req.Prompt.User, Model: cfg.Model.Name, Provider: providerName(cfg.Model.Provider), Mode: string(cfg.Model.Mode)}, nil
		}
	})

	invocation := func(script string) PromptInvocation {
		path := filepath.Join(dir, "classify.prompt")
		content := "---\nmodel: api:sonnet\nfanout:\n  exec: |\n    " + strings.ReplaceAll(script, "\n", "\n    ") +
			"\n  concurrency: 2\n---\nClassify {{item.name}} from {{caller}}.\n"
		Expect(os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
		return PromptInvocation{Reference: path, Options: AIPromptOptions{Vars: `{"caller":"test"}`}, Stdin: strings.NewReader("")}
	}

	It("runs the command once and returns fresh sessions in array order", func() {
		withGinkgoCaptainDB()
		marker := filepath.Join(dir, "command-invocations")
		request := invocation("#!/bin/sh\n" + `cat >> command-invocations` + "\n" +
			`printf '%s' '[{"name":"second"},{"name":"first"}]'`)
		request.Options.Spec = &api.Spec{}
		request.Options.Spec.SetCwd(dir)
		preview, err := RenderPrompt(context.Background(), request)
		Expect(err).NotTo(HaveOccurred())
		Expect(preview.Name).To(Equal("classify"))
		Expect(marker).NotTo(BeAnExistingFile())
		result, err := RunPrompt(context.Background(), request)
		Expect(err).NotTo(HaveOccurred())
		Expect(os.ReadFile(marker)).To(Equal([]byte(`{"caller":"test"}`)))
		Expect(result.Runs).To(HaveExactElements(
			SatisfyAll(HaveField("Text", "Classify second from test."), HaveField("SessionID", Not(BeEmpty()))),
			SatisfyAll(HaveField("Text", "Classify first from test."), HaveField("SessionID", Not(BeEmpty()))),
		))
		Expect(result.Runs[0].SessionID).NotTo(Equal(result.Runs[1].SessionID))
		db, err := captainDefaultDB(context.Background())
		Expect(err).NotTo(HaveOccurred())
		rootID := uuid.MustParse(result.BatchID)
		for _, run := range result.Runs {
			child, err := db.GetSession(context.Background(), uuid.MustParse(run.SessionID))
			Expect(err).NotTo(HaveOccurred())
			Expect(child).To(SatisfyAll(HaveField("ParentSessionID", &rootID), HaveField("InitialPrompt", run.Text)))
		}
	})

	DescribeTable("requires exactly one JSON array on stdout", func(output, want string) {
		result, err := RunPrompt(context.Background(), invocation(`printf '%s' '`+output+`'`))
		Expect(err).To(MatchError(ContainSubstring(want)))
		Expect(result.Runs).To(BeEmpty())
	},
		Entry("an object", `{"name":"first"}`, "JSON array"),
		Entry("null", "null", "JSON array"),
		Entry("multiple documents", "[] []", "JSON array"),
	)

	It("treats an empty array as a successful run with no sessions", func() {
		result, err := RunPrompt(context.Background(), invocation(`printf '%s' '[]'`))
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Status).To(Equal("completed"))
		Expect(result.Runs).To(BeEmpty())
	})

	It("does not launch children when the command exits unsuccessfully", func() {
		result, err := RunPrompt(context.Background(), invocation("printf '[]'\nprintf 'cannot enumerate' >&2\nexit 7"))
		Expect(err).To(MatchError(And(ContainSubstring("exited with 7"), ContainSubstring("cannot enumerate"))))
		Expect(result.BatchID).To(BeEmpty())
	})

	It("previews an HTTP draft without executing its command, then applies fan-out when run", func() {
		content, err := os.ReadFile(invocation(`printf '%s' '{"unexpected":"object"}'`).Reference)
		Expect(err).NotTo(HaveOccurred())
		body, err := json.Marshal(PromptRenderRequest{Content: string(content), Variables: map[string]any{"caller": "test"}})
		Expect(err).NotTo(HaveOccurred())
		requestContext := func(action string) context.Context {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/prompt/"+action, strings.NewReader(string(body)))
			request.Header.Set("Content-Type", "application/json")
			return clickyrpc.ContextWithRequest(context.Background(), request)
		}
		preview, err := renderPromptAction(requestContext("render"), "", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(preview.FanOut).NotTo(BeNil())
		result, err := runPromptAction(requestContext("run"), "", nil)
		Expect(err).To(MatchError(ContainSubstring("must print one JSON array")))
		Expect(result.Runs).To(BeEmpty())
	})

	It("launches a trackable session per item and bounds async concurrency", func(ctx SpecContext) {
		withGinkgoCaptainDB()
		GinkgoT().Setenv("ANTHROPIC_API_KEY", "test-key")
		configs, release, complete := make(chan ai.Config, 3), make(chan struct{}), make(chan PromptRunSummary, 1)
		runtime := ai.RuntimeOf(ai.Anthropic, ai.ModeAPI)
		ai.RegisterProvider(runtime, func(cfg ai.Config) (ai.Provider, error) {
			configs <- cfg
			return launchFakeProvider{release: release, output: `{"message":"done"}`}, nil
		})
		DeferCleanup(func() {
			ai.RegisterProvider(runtime, func(cfg ai.Config) (ai.Provider, error) { return genkit.New(cfg) })
		})
		defer func() {
			select {
			case <-release:
			default:
				close(release)
			}
		}()
		request := invocation(`printf '%s' '[{"name":"docs"},{"name":"tests"},{"name":"examples"}]'`)
		request.OnComplete = func(summary PromptRunSummary) { complete <- summary }

		result, err := LaunchPrompt(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.BatchID).NotTo(BeEmpty())
		Expect(result.Runs).To(HaveExactElements(HaveField("FanOutIndex", 1), HaveField("FanOutIndex", 2), HaveField("FanOutIndex", 3)))
		Eventually(configs).Should(Receive())
		Eventually(configs).Should(Receive())
		Consistently(configs).ShouldNot(Receive())
		close(release)
		for _, child := range result.Runs {
			summary, err := AwaitPromptRun(ctx, child.RunID)
			Expect(err).NotTo(HaveOccurred())
			Expect(summary.SessionID).To(Equal(child.SessionID))
		}
		Eventually(complete).Should(Receive(HaveField("Success", true)))
	}, SpecTimeout(15*time.Second))
})
