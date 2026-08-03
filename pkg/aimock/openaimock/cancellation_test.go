package openaimock

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHeldStreamsRecordCancellationWithoutAMiss(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
		body map[string]any
	}{
		{name: "responses", path: "/v1/responses", body: map[string]any{
			"model": "gpt-5", "stream": true, "input": userInput("wait for interruption"),
		}},
		{name: "chat completions", path: "/v1/chat/completions", body: map[string]any{
			"model": "gpt-5", "stream": true,
			"messages": []any{map[string]any{"role": "user", "content": "wait for interruption"}},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv := startServer(t, "hold-open.yaml")
			raw, err := json.Marshal(test.body)
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL()+test.path, bytes.NewReader(raw))
			require.NoError(t, err)
			request.Header.Set("Content-Type", "application/json")
			response, err := http.DefaultClient.Do(request)
			require.NoError(t, err)
			cancel()
			_, _ = io.ReadAll(response.Body)
			_ = response.Body.Close()
			require.Eventually(t, func() bool { return len(srv.Requests()) == 1 }, time.Second, 10*time.Millisecond)
			assert.True(t, srv.Requests()[0].Cancelled)
			assert.Empty(t, srv.Requests()[0].Miss)
		})
	}
}
