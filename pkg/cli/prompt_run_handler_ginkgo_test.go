package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/rpc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"
)

var _ = Describe("PromptRunHandler for embedding hosts", func() {
	const runID = "run-embed-7c1e"
	var server *httptest.Server

	BeforeEach(func() {
		stream := promptRuns.create(runID)
		DeferCleanup(promptRuns.remove, runID)
		stream.setRun(PromptRunFrame{RunID: runID, SessionID: "session-3f0a", Status: "running", Model: "claude-sonnet-5"})
		stream.complete(PromptRunSummary{RunID: runID, SessionID: "session-3f0a", Success: true, Text: "merged"})

		mux := http.NewServeMux()
		mux.Handle("/api/captain/prompt/runs/", http.StripPrefix("/api/captain/prompt/runs", PromptRunHandler()))
		server = httptest.NewServer(mux)
		DeferCleanup(server.Close)
	})

	It("serves a run's snapshot under the host's mount point", func() {
		resp, err := http.Get(server.URL + "/api/captain/prompt/runs/" + runID)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		var snapshot struct {
			Run     PromptRunFrame   `json:"run"`
			Done    bool             `json:"done"`
			Summary PromptRunSummary `json:"summary"`
		}
		Expect(json.NewDecoder(resp.Body).Decode(&snapshot)).To(Succeed())
		Expect(snapshot.Run.SessionID).To(Equal("session-3f0a"))
		Expect(snapshot.Done).To(BeTrue())
		Expect(snapshot.Summary.Text).To(Equal("merged"))
	})

	It("streams the run frame carrying the session id a host follows", func() {
		resp, err := http.Get(server.URL + "/api/captain/prompt/runs/" + runID + "/stream")
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close()
		Expect(resp.Header.Get("Content-Type")).To(HavePrefix("text/event-stream"))
		body := make([]byte, 4096)
		n, _ := resp.Body.Read(body)
		Expect(string(body[:n])).To(ContainSubstring(`event: run`))
		Expect(string(body[:n])).To(ContainSubstring(`"sessionId":"session-3f0a"`))
	})

	It("answers 404 for a run this process does not hold", func() {
		resp, err := http.Get(server.URL + "/api/captain/prompt/runs/run-unknown")
		Expect(err).NotTo(HaveOccurred())
		resp.Body.Close()
		Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
	})

	It("starts runs at POST <prefix>/prompt/run with the prompt as the id query parameter", func() {
		RegisterPromptEntity()
		root := &cobra.Command{Use: "host"}
		clicky.GenerateCLI(root)
		openAPI := &rpc.OpenAPIConfig{Title: "host", Version: "test"}
		server := rpc.NewSwaggerServer(&rpc.ServeConfig{Title: "host", Version: "test",
			Executor: &rpc.ExecutorConfig{Enabled: true, SkipPreRun: true, PathPrefix: "/api/v1"}}, root, openAPI)
		spec, err := rpc.NewOpenAPIGenerator(openAPI).GenerateFromCobraWithConfig(root, server.ConverterConfig())
		Expect(err).NotTo(HaveOccurred())
		Expect(spec.Paths).To(HaveKeyWithValue("/api/v1/prompt/run", HaveKey("post")))
	})
})

var _ = Describe("a live run's session id", func() {
	It("is republished on a run frame as soon as the provider reports it", func() {
		stream := newRunStream()
		stream.setRun(PromptRunFrame{RunID: "run-live-51d2", Status: "running"})
		_, events := stream.subscribeEvents()
		DeferCleanup(stream.unsubscribeEvents, events)

		acc := newPromptEventAccumulator(stream.publish, fakeTaskSink{}, "m", "b")
		acc.session = stream.setRunMetadata
		acc.handle(0, ai.Event{Kind: ai.EventSystem, SessionID: "provider-9e44"})

		var frame runStreamEvent
		Eventually(events).Should(Receive(&frame))
		Expect(frame.name).To(Equal("run"))
		Expect(frame.data).To(HaveField("SessionID", "provider-9e44"))
	})
})

var _ = Describe("workflowConfigured", func() {
	It("routes a workflow that declares only after steps through the runner", func() {
		Expect(workflowConfigured(&api.Workflow{After: []api.AfterStep{{Commands: []string{"true"}}}})).To(BeTrue())
	})
})
