// Tests that a streamed request is in the journal before its last frame reaches the client, so a
// client that has read the whole stream finds it in Requests().
package openaimock

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamedRequestsAreRecordedBeforeTheirLastFrame(t *testing.T) {
	for _, test := range []struct {
		name, path, last string
		body             map[string]any
		handle           func(*Server) http.HandlerFunc
	}{
		{name: "responses", path: "/v1/responses", last: "event: response.completed", body: map[string]any{
			"model": "gpt-5", "stream": true, "input": userInput("What is the capital of France?"),
		}, handle: func(s *Server) http.HandlerFunc { return s.handleResponses }},
		{name: "chat completions", path: "/v1/chat/completions", last: "data: [DONE]", body: map[string]any{
			"model": "gpt-5", "stream": true,
			"messages": []any{map[string]any{"role": "user", "content": "What is the capital of France?"}},
		}, handle: func(s *Server) http.HandlerFunc { return s.handleChatCompletions }},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv := startServer(t, "text-only.yaml")
			raw, err := json.Marshal(test.body)
			require.NoError(t, err)
			probe := &lastFrameProbe{ResponseRecorder: httptest.NewRecorder(), last: test.last, recorded: func() int { return len(srv.Requests()) }}

			test.handle(srv)(probe, httptest.NewRequest(http.MethodPost, test.path, bytes.NewReader(raw)))

			require.True(t, probe.sent, "the stream ended with %q", test.last)
			assert.Equal(t, 1, probe.recordedBefore, "the request was recorded when %q reached the client", test.last)
		})
	}
}

// lastFrameProbe counts the recorded requests when the frame starting with last is flushed to the
// client.
type lastFrameProbe struct {
	*httptest.ResponseRecorder
	last           string
	recorded       func() int
	sent           bool
	recordedBefore int
}

func (p *lastFrameProbe) Flush() {
	p.ResponseRecorder.Flush()
	if !p.sent && strings.Contains(p.Body.String(), p.last) {
		p.sent, p.recordedBefore = true, p.recorded()
	}
}
