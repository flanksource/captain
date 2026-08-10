package aichat_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	_ "github.com/flanksource/captain/pkg/ai/provider"
	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/aimock"
	"github.com/flanksource/captain/pkg/aimock/anthropicmock"
	"github.com/flanksource/captain/pkg/aimock/openaimock"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/commons-db/dbtest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type lifecycleRuntime struct {
	name      string
	model     api.Model
	protocol  string
	agent     bool
	binaries  []string
	scenario  string
	configEnv string
}

type lifecycleMock struct {
	server    aimock.Server
	apiURL    string
	remaining func() []string
}

type lifecycleRoundTripFunc func(*http.Request) (*http.Response, error)

func (f lifecycleRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type lifecycleSignalReadCloser struct {
	io.ReadCloser
	match  string
	signal chan struct{}
	once   sync.Once
	read   strings.Builder
}

func (r *lifecycleSignalReadCloser) Read(buffer []byte) (int, error) {
	read, err := r.ReadCloser.Read(buffer)
	if read > 0 {
		r.read.Write(buffer[:read])
		if strings.Contains(r.read.String(), r.match) {
			r.once.Do(func() { close(r.signal) })
		}
	}
	return read, err
}

type realChatResolver struct{}

func (realChatResolver) Models(context.Context) (aichat.ModelCatalogResponse, error) {
	return nil, nil
}

func (realChatResolver) Runtimes(context.Context) ([]api.RuntimeFamily, error) {
	return api.RuntimeCatalog(), nil
}

func (realChatResolver) Provider(_ context.Context, config api.Config) (api.StreamingProvider, error) {
	provider, err := ai.NewProvider(config)
	if err != nil {
		return nil, err
	}
	streaming, ok := api.ProviderAs[api.StreamingProvider](provider)
	if !ok {
		return nil, fmt.Errorf("backend %q is not streaming", provider.GetBackend())
	}
	return streaming, nil
}

type httpResult struct {
	status int
	body   []byte
	err    error
}

var _ = Describe("Mocked Captain chat lifecycle", func() {
	DescribeTable("persists request, response, approval, interruption, and resume",
		func(ctx SpecContext, runtime lifecycleRuntime) {
			if runtime.agent {
				if os.Getenv("CAPTAIN_AIMOCK_AGENT_E2E") != "1" {
					Skip("set CAPTAIN_AIMOCK_AGENT_E2E=1 to run real agent-process lifecycle tests")
				}
				for _, binary := range runtime.binaries {
					_, err := exec.LookPath(binary)
					Expect(err).NotTo(HaveOccurred(), "%s is required when the agent E2E gate is enabled", binary)
				}
				GinkgoT().Setenv(runtime.configEnv, GinkgoT().TempDir())
			}
			GinkgoT().Setenv(api.MonitorHooksEnv, "off")

			mock := startLifecycleMock(runtime)
			DeferCleanup(mock.server.Close)
			dbName := "captain_aichat_mock_" + strings.NewReplacer(" ", "_", "-", "_").Replace(runtime.name)
			testDB := dbtest.ForGinkgo(dbtest.Options{Name: dbName})
			db, err := database.Open(ctx, database.WithDSN(testDB.DSN()), database.WithMigrations())
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(db.Close)
			store, err := aichat.NewDatabaseThreadStore(db)
			Expect(err).NotTo(HaveOccurred())
			authority, err := aichat.NewDatabaseExecutionAuthority(db)
			Expect(err).NotTo(HaveOccurred())

			var toolCalls atomic.Int32
			var inputMu sync.Mutex
			var approvedInput map[string]any
			service := aichat.NewService(aichat.ServiceOptions{
				Resolver: realChatResolver{}, Threads: aichat.FixedThreadStore(store), Authority: authority,
				Settings: aichat.RuntimeSettingsProviderFunc(func(context.Context) (aichat.RuntimeSettings, error) {
					return aichat.RuntimeSettings{ProviderConfig: api.Config{
						APIURL: mock.apiURL, APIKey: aimock.DummyKey,
					}}, nil
				}),
				Tools: aichat.StaticToolProvider([]api.ToolDefinition{{
					Name: "accounts_edit", DefaultPermission: api.ToolModeAsk,
					Handler: func(_ context.Context, input map[string]any) (any, error) {
						toolCalls.Add(1)
						inputMu.Lock()
						approvedInput = input
						inputMu.Unlock()
						return map[string]any{"id": input["id"], "name": input["name"], "updated": true}, nil
					},
				}}),
			})
			server := httptest.NewServer(service.Handler())
			DeferCleanup(server.Close)
			client := server.Client()

			responseSession := createLifecycleSession(ctx, client, server.URL, "Response")
			response := sendLifecycleChat(ctx, client, server.URL, responseSession.ID, runtime.model, nil,
				"user-response", "Return the lifecycle greeting")
			Expect(response.err).NotTo(HaveOccurred())
			Expect(response.status).To(Equal(http.StatusOK), string(response.body))
			Expect(lifecycleSSEText(response.body)).To(Equal("Lifecycle response complete."), string(response.body))
			assertCompletedSession(ctx, client, server.URL, responseSession.ID, runtime, "Lifecycle response complete.")

			approved := runApprovalFlow(ctx, client, server.URL, mock.server, runtime.model, "Approve", true, "approved by test")
			Expect(approved.Requests).To(HaveLen(1))
			Expect(approved.Requests[0].State).To(Equal(string(database.TurnRequestStateApproved)))
			Expect(toolCalls.Load()).To(Equal(int32(1)))
			inputMu.Lock()
			Expect(approvedInput).To(Equal(map[string]any{"id": "acc-1", "name": "Approved Account"}))
			inputMu.Unlock()

			rejected := runApprovalFlow(ctx, client, server.URL, mock.server, runtime.model, "Reject", false, "rejected by test")
			Expect(rejected.Requests).To(HaveLen(1))
			Expect(rejected.Requests[0].State).To(Equal(string(database.TurnRequestStateDenied)))
			Expect(rejected.Requests[0].Reason).To(Equal("rejected by test"))
			Expect(toolCalls.Load()).To(Equal(int32(1)), "a rejected tool must not execute")

			interruptSession := createLifecycleSession(ctx, client, server.URL, "Interrupt")
			chatResult := make(chan httpResult, 1)
			responseStarted := make(chan struct{})
			streamClient := *client
			transport := client.Transport
			if transport == nil {
				transport = http.DefaultTransport
			}
			streamClient.Transport = lifecycleRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				response, roundTripErr := transport.RoundTrip(request)
				if roundTripErr == nil {
					response.Body = &lifecycleSignalReadCloser{
						ReadCloser: response.Body, match: "Partial", signal: responseStarted,
					}
				}
				return response, roundTripErr
			})
			go func() {
				chatResult <- sendLifecycleChat(ctx, &streamClient, server.URL, interruptSession.ID, runtime.model, nil,
					"user-interrupt", "Wait for the lifecycle interrupt")
			}()
			Eventually(responseStarted).WithTimeout(30 * time.Second).Should(BeClosed())
			Eventually(func(g Gomega) {
				aggregate := getLifecycleSession(ctx, client, server.URL, interruptSession.ID)
				g.Expect(aggregate.LifecycleStatus).To(Equal(string(database.SessionLifecycleRunning)))
				if runtime.agent {
					g.Expect(aggregate.ProviderSessionID).NotTo(BeEmpty())
				}
			}).WithTimeout(30 * time.Second).Should(Succeed())
			interruptedProviderID := getLifecycleSession(ctx, client, server.URL, interruptSession.ID).ProviderSessionID
			interruptContext, cancelInterrupt := context.WithTimeout(ctx, 5*time.Second)
			defer cancelInterrupt()
			interrupt := postLifecycleJSON(interruptContext, client, http.MethodPost,
				server.URL+"/api/chat/sessions/"+interruptSession.ID+"/interrupt", nil)
			Expect(interrupt.err).NotTo(HaveOccurred())
			Expect(interrupt.status).To(Equal(http.StatusOK), string(interrupt.body))
			var interrupted httpResult
			Eventually(chatResult).WithTimeout(30 * time.Second).Should(Receive(&interrupted))
			Expect(interrupted.err).NotTo(HaveOccurred())
			Expect(string(interrupted.body)).To(ContainSubstring(`"interrupted":true`))
			Expect(string(interrupted.body)).NotTo(ContainSubstring(`"type":"error"`))

			interruptedSession := getLifecycleSession(ctx, client, server.URL, interruptSession.ID)
			Expect(interruptedSession.LifecycleStatus).To(Equal(string(database.SessionLifecycleInterrupted)))
			Expect(interruptedSession.ActivityState).To(Equal(string(database.SessionActivityIdle)))
			Expect(interruptedSession.Turns).To(HaveLen(1))
			Expect(interruptedSession.Turns[0].Status).To(Equal(string(database.TurnStatusInterrupted)))
			Expect(interruptedSession.Turns[0].StopReason).To(Equal("interrupt"))

			resumedMessages := lifecycleMessages(interruptedSession.Messages)
			resumed := sendLifecycleChat(ctx, client, server.URL, interruptSession.ID, runtime.model, resumedMessages,
				"user-resume", "Resume after the lifecycle interrupt")
			Expect(resumed.err).NotTo(HaveOccurred())
			Expect(resumed.status).To(Equal(http.StatusOK), string(resumed.body))
			finalSession := getLifecycleSession(ctx, client, server.URL, interruptSession.ID)
			Expect(finalSession.LifecycleStatus).To(Equal(string(database.SessionLifecycleSucceeded)))
			Expect(finalSession.ActivityState).To(Equal(string(database.SessionActivityIdle)))
			Expect(finalSession.Turns).To(HaveLen(2))
			Expect(finalSession.Turns[0].Status).To(Equal(string(database.TurnStatusInterrupted)))
			Expect(finalSession.Turns[1].Status).To(Equal(string(database.TurnStatusEnded)))
			if runtime.agent {
				Expect(interruptedProviderID).NotTo(BeEmpty())
				Expect(finalSession.ProviderSessionID).To(Equal(interruptedProviderID))
			} else {
				Expect(finalSession.ProviderSessionID).To(BeEmpty())
			}

			Eventually(func() bool {
				for _, request := range mock.server.Requests() {
					if strings.Contains(request.Request.LastUserText(), "Wait for the lifecycle interrupt") {
						return request.Cancelled && request.Miss == ""
					}
				}
				return false
			}).Should(BeTrue())
			Expect(mock.remaining()).To(BeEmpty())
			for _, request := range mock.server.Requests() {
				Expect(request.Miss).To(BeEmpty(), "%s %s", request.Method, request.Path)
			}
		},
		Entry("Anthropic API", lifecycleRuntime{
			name: "anthropic_api", model: api.Model{Name: "claude-sonnet-4-6", Backend: api.BackendAnthropic, Mode: api.ModeAPI},
			protocol: aimock.SectionAnthropic, scenario: "chat-api-flows.yaml",
		}),
		Entry("OpenAI API", lifecycleRuntime{
			name: "openai_api", model: api.Model{Name: "gpt-5", Backend: api.BackendOpenAI, Mode: api.ModeAPI},
			protocol: aimock.SectionOpenAI, scenario: "chat-api-flows.yaml",
		}),
		Entry("Claude Agent", lifecycleRuntime{
			name: "claude_agent", model: api.Model{Name: "claude-sonnet-5", Backend: api.BackendClaudeAgent, Mode: api.ModeAgent},
			protocol: aimock.SectionAnthropic, agent: true, binaries: []string{"npm", "claude"},
			scenario: "chat-agent-flows.yaml", configEnv: "CLAUDE_CONFIG_DIR",
		}),
		Entry("Codex Agent", lifecycleRuntime{
			name: "codex_agent", model: api.Model{Name: "gpt-5.6-sol", Backend: api.BackendCodexAgent, Mode: api.ModeAgent},
			protocol: aimock.SectionOpenAI, agent: true, binaries: []string{"codex"},
			scenario: "chat-agent-flows.yaml", configEnv: "CODEX_HOME",
		}),
	)
})

func startLifecycleMock(runtime lifecycleRuntime) lifecycleMock {
	scenario, err := aimock.Load(filepath.Join("..", "aimock", "testdata", "scenarios", runtime.scenario))
	Expect(err).NotTo(HaveOccurred())
	if runtime.protocol == aimock.SectionAnthropic {
		server, err := anthropicmock.Start(anthropicmock.Options{Scenario: scenario})
		Expect(err).NotTo(HaveOccurred())
		return lifecycleMock{server: server, apiURL: server.APIURL(), remaining: server.Remaining}
	}
	server, err := openaimock.Start(openaimock.Options{Scenario: scenario})
	Expect(err).NotTo(HaveOccurred())
	return lifecycleMock{server: server, apiURL: server.APIURL(), remaining: server.Remaining}
}

func createLifecycleSession(ctx context.Context, client *http.Client, baseURL, title string) aichat.Thread {
	result := postLifecycleJSON(ctx, client, http.MethodPost, baseURL+"/api/chat/sessions", map[string]string{"title": title})
	Expect(result.err).NotTo(HaveOccurred())
	Expect(result.status).To(Equal(http.StatusCreated), string(result.body))
	var thread aichat.Thread
	Expect(json.Unmarshal(result.body, &thread)).To(Succeed())
	return thread
}

func sendLifecycleChat(
	ctx context.Context,
	client *http.Client,
	baseURL, sessionID string,
	model api.Model,
	messages []aichat.UIMessage,
	messageID, prompt string,
) httpResult {
	messages = append(messages, aichat.UIMessage{
		ID: messageID, Role: string(api.RoleUser), Parts: []aichat.UIPart{{Type: "text", Text: prompt}},
	})
	return postLifecycleJSON(ctx, client, http.MethodPost, baseURL+"/api/chat", aichat.ChatRequest{
		ID: sessionID, ThreadID: sessionID, Trigger: "submit-message", Runtime: &model, Messages: messages,
	})
}

func runApprovalFlow(
	ctx context.Context,
	client *http.Client,
	baseURL string,
	mockServer aimock.Server,
	model api.Model,
	verb string,
	approved bool,
	reason string,
) session.Session {
	thread := createLifecycleSession(ctx, client, baseURL, verb)
	chatResult := make(chan httpResult, 1)
	go func() {
		chatResult <- sendLifecycleChat(ctx, client, baseURL, thread.ID, model, nil,
			"user-"+strings.ToLower(verb), verb+" the account update")
	}()
	var pending session.Session
	// The chat result is consumed at most once across poll retries: a value
	// drained inside a failed Eventually iteration would otherwise be lost and
	// the final receive below would wait on an already-empty channel.
	var chat httpResult
	chatReceived := false
	Eventually(func(g Gomega) {
		pending = getLifecycleSession(ctx, client, baseURL, thread.ID)
		if len(pending.Requests) == 0 {
			if !chatReceived {
				select {
				case chat = <-chatResult:
					chatReceived = true
				default:
				}
			}
			if chatReceived {
				g.Expect(chat.err).NotTo(HaveOccurred())
				g.Expect(chat.status).To(Equal(http.StatusOK), string(chat.body))
				g.Expect(pending.Requests).To(HaveLen(1), string(chat.body))
			} else {
				g.Expect(pending.Requests).To(HaveLen(1), lifecycleRequestsJSON(mockServer.Requests()))
			}
			return
		}
		g.Expect(pending.Requests).To(HaveLen(1))
		g.Expect(pending.Requests[0].State).To(Equal(string(database.TurnRequestStatePending)))
	}).WithTimeout(30 * time.Second).Should(Succeed())
	body := map[string]any{"approved": approved, "reason": reason}
	if approved {
		body["updatedInput"] = map[string]any{"id": "acc-1", "name": "Approved Account"}
	}
	decision := postLifecycleJSON(ctx, client, http.MethodPost,
		baseURL+"/api/chat/sessions/"+thread.ID+"/approvals/"+pending.Requests[0].ID, body)
	Expect(decision.err).NotTo(HaveOccurred())
	Expect(decision.status).To(Equal(http.StatusOK), string(decision.body))
	var completed session.Session
	Eventually(func(g Gomega) {
		completed = getLifecycleSession(ctx, client, baseURL, thread.ID)
		g.Expect(completed.Turns).To(HaveLen(1))
		g.Expect(completed.Turns[0].Status).To(Equal(string(database.TurnStatusEnded)),
			lifecycleRequestsJSON(mockServer.Requests()))
	}).WithTimeout(30 * time.Second).Should(Succeed())
	if !chatReceived {
		Eventually(chatResult).WithTimeout(30 * time.Second).Should(Receive(&chat))
	}
	Expect(chat.err).NotTo(HaveOccurred())
	Expect(chat.status).To(Equal(http.StatusOK), string(chat.body))
	conflict := postLifecycleJSON(ctx, client, http.MethodPost,
		baseURL+"/api/chat/sessions/"+thread.ID+"/approvals/"+pending.Requests[0].ID,
		map[string]any{"approved": !approved, "reason": "conflicting replay"})
	Expect(conflict.err).NotTo(HaveOccurred())
	Expect(conflict.status).To(Equal(http.StatusConflict), string(conflict.body))
	return completed
}

func assertCompletedSession(
	ctx context.Context,
	client *http.Client,
	baseURL, sessionID string,
	runtime lifecycleRuntime,
	text string,
) {
	aggregate := getLifecycleSession(ctx, client, baseURL, sessionID)
	Expect(aggregate.LifecycleStatus).To(Equal(string(database.SessionLifecycleSucceeded)))
	Expect(aggregate.ActivityState).To(Equal(string(database.SessionActivityIdle)))
	Expect(aggregate.ExecutionMode).To(Equal(runtime.model.Mode))
	Expect(aggregate.Backend).To(Equal(string(runtime.model.Backend)))
	Expect(aggregate.Model).To(Equal(runtime.model.Name))
	Expect(aggregate.Turns).To(HaveLen(1))
	Expect(aggregate.Turns[0].Status).To(Equal(string(database.TurnStatusEnded)))
	Expect(aggregate.Turns[0].StopReason).To(Equal("stop"))
	encoded, err := json.Marshal(aggregate.Messages)
	Expect(err).NotTo(HaveOccurred())
	Expect(string(encoded)).To(ContainSubstring(text))
	Expect(aggregate.Usage.TotalTokens()).To(BeNumerically(">", 0))
}

func getLifecycleSession(ctx context.Context, client *http.Client, baseURL, id string) session.Session {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/chat/sessions/"+id, nil)
	Expect(err).NotTo(HaveOccurred())
	response, err := client.Do(request)
	Expect(err).NotTo(HaveOccurred())
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	Expect(err).NotTo(HaveOccurred())
	Expect(response.StatusCode).To(Equal(http.StatusOK), string(body))
	var aggregate session.Session
	Expect(json.Unmarshal(body, &aggregate)).To(Succeed())
	return aggregate
}

func lifecycleMessages(messages []session.Message) []aichat.UIMessage {
	encoded, err := json.Marshal(messages)
	Expect(err).NotTo(HaveOccurred())
	var output []aichat.UIMessage
	Expect(json.Unmarshal(encoded, &output)).To(Succeed())
	return output
}

func lifecycleSSEText(body []byte) string {
	var textValue strings.Builder
	for _, line := range strings.Split(string(body), "\n") {
		payload := strings.TrimPrefix(line, "data: ")
		if payload == line || payload == "[DONE]" {
			continue
		}
		part := struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		}{}
		if json.Unmarshal([]byte(payload), &part) == nil && part.Type == "text-delta" {
			textValue.WriteString(part.Delta)
		}
	}
	return textValue.String()
}

func lifecycleRequestsJSON(requests []aimock.Recorded) string {
	type requestDiagnostic struct {
		Path           string                     `json:"path"`
		LastUserText   string                     `json:"lastUserText,omitempty"`
		ToolResults    []string                   `json:"toolResults,omitempty"`
		ToolNames      []string                   `json:"toolNames,omitempty"`
		MCPTools       map[string]json.RawMessage `json:"mcpTools,omitempty"`
		MCPDefinitions map[string]json.RawMessage `json:"mcpDefinitions,omitempty"`
		Miss           string                     `json:"miss,omitempty"`
		Cancelled      bool                       `json:"cancelled,omitempty"`
	}
	diagnostics := make([]requestDiagnostic, 0, len(requests))
	for _, request := range requests {
		diagnostic := requestDiagnostic{
			Path: request.Path, LastUserText: request.Request.LastUserText(),
			ToolResults: request.Request.ToolResultNames(),
			ToolNames:   request.Request.ToolNames, Miss: request.Miss, Cancelled: request.Cancelled,
		}
		for name, schema := range request.Request.ToolSchemas {
			if strings.HasPrefix(name, "mcp__") {
				if diagnostic.MCPTools == nil {
					diagnostic.MCPTools = map[string]json.RawMessage{}
				}
				diagnostic.MCPTools[name] = schema
			}
		}
		for name, definition := range request.Request.ToolDefinitions {
			if strings.HasPrefix(name, "mcp__") {
				if diagnostic.MCPDefinitions == nil {
					diagnostic.MCPDefinitions = map[string]json.RawMessage{}
				}
				diagnostic.MCPDefinitions[name] = definition
			}
		}
		diagnostics = append(diagnostics, diagnostic)
	}
	encoded, err := json.MarshalIndent(diagnostics, "", "  ")
	if err != nil {
		return err.Error()
	}
	return string(encoded)
}

func postLifecycleJSON(ctx context.Context, client *http.Client, method, url string, body any) httpResult {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return httpResult{err: err}
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, url, payload)
	if err != nil {
		return httpResult{err: err}
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return httpResult{err: err}
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	return httpResult{status: response.StatusCode, body: responseBody, err: err}
}
