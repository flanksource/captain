// Tests that a streamed request is in the journal before its last frame reaches the client, so a
// client that has read the whole stream finds it in Requests().
package anthropicmock

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

func TestAStreamedRequestIsRecordedBeforeItsLastFrame(t *testing.T) {
	srv := startServer(t, "text-only.yaml")
	raw, err := json.Marshal(map[string]any{
		"model": "claude-sonnet-5", "stream": true,
		"messages": []any{userTurn("What is the capital of France?")},
	})
	require.NoError(t, err)
	probe := &lastFrameProbe{ResponseRecorder: httptest.NewRecorder(), last: "event: message_stop", recorded: func() int { return len(srv.Requests()) }}

	srv.handleMessages(probe, httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(raw)))

	require.True(t, probe.sent, "the stream ended with message_stop")
	assert.Equal(t, 1, probe.recordedBefore, "the request was recorded when message_stop reached the client")
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
