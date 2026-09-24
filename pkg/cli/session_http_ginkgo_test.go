package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	sessionquery "github.com/flanksource/captain/pkg/session/query"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SessionHandler", Ordered, func() {
	const planMarkdown = "# Plan: shadow activities by age"
	var (
		db       *database.DB
		planned  *database.Session
		followed *database.Session
		server   *httptest.Server
	)

	BeforeAll(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_session_handler"})
		var err error
		db, err = database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		setCaptainDBForTest(db)
		DeferCleanup(func() {
			setCaptainDBForTest(nil)
			Expect(db.Close()).To(Succeed())
		})

		home, err := os.MkdirTemp("", "captain-session-handler-home-")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(os.RemoveAll(home)).To(Succeed()) })
		previousHome := os.Getenv("HOME")
		Expect(os.Setenv("HOME", home)).To(Succeed())
		DeferCleanup(func() { Expect(os.Setenv("HOME", previousHome)).To(Succeed()) })

		providerID := uuid.NewString()
		cwd := filepath.Join(home, "work")
		historyPath := writeExitPlanTranscript(cwd, providerID, planMarkdown)
		planned, err = db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: providerID, Source: "claude", Provider: "anthropic",
			HostID: captainHostID(), CWD: cwd, Path: historyPath,
		})
		Expect(err).NotTo(HaveOccurred())

		followed, err = db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: uuid.NewString(), Source: "claude", Provider: "anthropic", HostID: captainHostID(),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(db.PutChatMessage(ctx, database.PutChatMessageInput{
			SessionID: followed.ID, ProviderMessageID: "streamed-1", Role: "assistant",
			Parts: json.RawMessage(`[{"type":"text","text":"streamed over SSE"}]`),
		})).To(Succeed())

		mux := http.NewServeMux()
		registerSessionRoutes(mux, newChatBroker())
		server = httptest.NewServer(mux)
		DeferCleanup(server.Close)
	})

	It("returns the unified session with the plan the transcript's ExitPlanMode carried", func(ctx SpecContext) {
		response := getSession(ctx, server.URL+"/api/captain/sessions/"+planned.ID.String(), "application/json")
		defer response.Body.Close()

		Expect(response.StatusCode).To(Equal(http.StatusOK))
		Expect(response.Header.Get("Content-Type")).To(HavePrefix("application/json"))
		var body session.Session
		Expect(json.NewDecoder(response.Body).Decode(&body)).To(Succeed())
		Expect(body.ID).To(Equal(planned.ID.String()))
		Expect(body.Plan).NotTo(BeNil())
		Expect(body.Plan.Content).To(Equal(planMarkdown))
	})

	It("reports an unknown session as a JSON 404", func(ctx SpecContext) {
		response := getSession(ctx, server.URL+"/api/captain/sessions/"+uuid.NewString(), "application/json")
		defer response.Body.Close()

		Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		var body map[string]any
		Expect(json.NewDecoder(response.Body).Decode(&body)).To(Succeed())
		Expect(body["error"]).To(ContainSubstring("not found"))
	})

	It("reports a session nothing can describe as a 404 naming its detail sources", func(ctx SpecContext) {
		empty, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: uuid.NewString(), Source: "claude", Provider: "anthropic", HostID: captainHostID(),
		})
		Expect(err).NotTo(HaveOccurred())

		response := getSession(ctx, server.URL+"/api/captain/sessions/"+empty.ID.String(), "application/json")
		defer response.Body.Close()

		Expect(response.StatusCode).To(Equal(http.StatusNotFound))
		var body map[string]any
		Expect(json.NewDecoder(response.Body).Decode(&body)).To(Succeed())
		Expect(body).To(HaveKey("detailSource"))
		Expect(body["error"]).To(ContainSubstring(empty.ID.String()))
	})

	It("streams entry and state frames with ?follow=1", func(ctx SpecContext) {
		streamCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		response := getSession(streamCtx, server.URL+"/api/captain/sessions/"+followed.ID.String()+"?follow=1", "")
		defer response.Body.Close()

		Expect(response.StatusCode).To(Equal(http.StatusOK))
		Expect(response.Header.Get("Content-Type")).To(Equal("text/event-stream"))
		frames := readSSEFrames(response, 2)
		Expect(frames[0].event).To(Equal("entry"))
		var message session.Message
		Expect(json.Unmarshal([]byte(frames[0].data), &message)).To(Succeed())
		Expect(message.ID).To(Equal("streamed-1"))
		Expect(message.Parts[0].Text).To(Equal("streamed over SSE"))
		Expect(frames[1].event).To(Equal("state"))
		var state sessionquery.FollowState
		Expect(json.Unmarshal([]byte(frames[1].data), &state)).To(Succeed())
		Expect(state.Facets).To(MatchRegexp(`^[0-9a-f]{16}$`))
		state.Facets = ""
		Expect(state).To(Equal(sessionquery.FollowState{Revision: 0, LifecycleStatus: "created", ActivityState: "idle"}))
	})

	It("streams with Accept: text/event-stream", func(ctx SpecContext) {
		streamCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		response := getSession(streamCtx, server.URL+"/api/captain/sessions/"+followed.ID.String(), "text/event-stream")
		defer response.Body.Close()

		Expect(response.Header.Get("Content-Type")).To(Equal("text/event-stream"))
		Expect(readSSEFrames(response, 1)[0].event).To(Equal("entry"))
	})

	It("keeps the POST message route beside the mounted handler", func(ctx SpecContext) {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost,
			server.URL+"/api/captain/sessions/"+followed.ID.String()+"/message", strings.NewReader(`{"unknownField":true}`))
		Expect(err).NotTo(HaveOccurred())
		response, err := http.DefaultClient.Do(request)
		Expect(err).NotTo(HaveOccurred())
		defer response.Body.Close()

		// decodeChatRequest refuses the unknown field; SessionHandler would have
		// answered 405 for a POST.
		Expect(response.StatusCode).To(Equal(http.StatusBadRequest))
	})
})

func getSession(ctx context.Context, url, accept string) *http.Response {
	GinkgoHelper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	Expect(err).NotTo(HaveOccurred())
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	response, err := http.DefaultClient.Do(request)
	Expect(err).NotTo(HaveOccurred())
	return response
}

type sseFrame struct{ event, data string }

// readSSEFrames reads the first n event frames, skipping keep-alive comments.
func readSSEFrames(response *http.Response, n int) []sseFrame {
	GinkgoHelper()
	frames := make(chan sseFrame)
	go func() {
		defer GinkgoRecover()
		defer close(frames)
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		var current sseFrame
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				current.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				current.data = strings.TrimPrefix(line, "data: ")
			case line == "" && current.event != "":
				frames <- current
				current = sseFrame{}
			}
		}
	}()
	var collected []sseFrame
	timeout := time.After(10 * time.Second)
	for len(collected) < n {
		select {
		case frame, open := <-frames:
			Expect(open).To(BeTrue(), "stream ended after %d frames", len(collected))
			collected = append(collected, frame)
		case <-timeout:
			Fail("timed out waiting for SSE frames")
		}
	}
	return collected
}

// writeExitPlanTranscript writes a one-entry Claude log whose ExitPlanMode call
// carries the plan, where transcript discovery looks for it under $HOME.
func writeExitPlanTranscript(cwd, providerID, planMarkdown string) string {
	GinkgoHelper()
	historyPath := filepath.Join(claude.GetProjectsDir(), claude.NormalizePath(cwd), providerID+".jsonl")
	entry, err := json.Marshal(map[string]any{
		"type": "assistant", "sessionId": providerID, "uuid": "assistant-plan",
		"timestamp": "2026-09-24T10:00:00Z", "cwd": cwd, "slug": "shadow-activities",
		"message": map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "tool-plan", "name": "ExitPlanMode",
				"input": map[string]any{"planFilePath": filepath.Join(cwd, "plan.md"), "plan": planMarkdown}},
		}},
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(os.MkdirAll(filepath.Dir(historyPath), 0o755)).To(Succeed())
	Expect(os.WriteFile(historyPath, append(entry, '\n'), 0o644)).To(Succeed())
	return historyPath
}
