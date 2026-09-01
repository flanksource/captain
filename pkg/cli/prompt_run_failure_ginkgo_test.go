package cli

import (
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("prompt run failures", func() {
	It("retains run metadata in the terminal snapshot", func() {
		broker := &runBroker{runs: map[string]*runStream{}}
		stream := broker.create("run-4a82")
		stream.setRun(PromptRunFrame{
			RunID:     "run-4a82",
			SessionID: "session-91bd",
			Status:    "running",
			Model:     "claude-sonnet-5",
			Provider:  "anthropic",
			Mode:      "api",
		})
		stream.fail("provider rejected the request")

		request := httptest.NewRequest(http.MethodGet, "/api/captain/prompt/runs/run-4a82", nil)
		request.SetPathValue("runId", "run-4a82")
		response := httptest.NewRecorder()
		handlePromptRunSnapshot(broker)(response, request)

		Expect(response.Code).To(Equal(http.StatusOK))
		Expect(response.Body.String()).To(MatchJSON(`{
			"run": {
				"runId": "run-4a82",
				"sessionId": "session-91bd",
				"status": "error",
				"chat": false,
				"model": "claude-sonnet-5",
				"provider": "anthropic",
				"mode": "api",
				"capabilities": {
					"interrupt": false,
					"steer": false,
					"followUp": false,
					"resume": false
				}
			},
			"entries": null,
			"done": true,
			"summary": {
				"runId": "run-4a82",
				"sessionId": "session-91bd",
				"model": "claude-sonnet-5",
				"provider": "anthropic",
				"mode": "api",
				"success": false,
				"error": "provider rejected the request"
			},
			"error": "provider rejected the request"
		}`))
	})

	It("streams the failure summary instead of a bare error", func() {
		broker := &runBroker{runs: map[string]*runStream{}}
		stream := broker.create("run-4a82")
		stream.setRun(PromptRunFrame{
			RunID:     "run-4a82",
			SessionID: "session-91bd",
			Status:    "running",
			Model:     "claude-sonnet-5",
			Provider:  "anthropic",
			Mode:      "api",
		})
		stream.fail("provider rejected the request")

		request := httptest.NewRequest(http.MethodGet, "/api/captain/prompt/runs/run-4a82/stream", nil)
		request.SetPathValue("runId", "run-4a82")
		response := httptest.NewRecorder()
		handlePromptRunStream(broker)(response, request)

		Expect(response.Body.String()).To(Equal(
			"event: run\n" +
				`data: {"runId":"run-4a82","sessionId":"session-91bd","status":"error","chat":false,"model":"claude-sonnet-5","provider":"anthropic","mode":"api","capabilities":{"interrupt":false,"steer":false,"followUp":false,"resume":false}}` + "\n\n" +
				"event: error\n" +
				`data: {"runId":"run-4a82","sessionId":"session-91bd","model":"claude-sonnet-5","provider":"anthropic","mode":"api","success":false,"error":"provider rejected the request"}` + "\n\n",
		))
	})
})
