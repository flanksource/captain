package session

import (
	"encoding/json"
	"strings"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Codex transcript context", func() {
	It("projects native context independently of aggregate billing metadata", func() {
		s := &Session{Context: &api.ContextUsage{UsedTokens: 89595, WindowTokens: 258400, FreePercent: 69}, Usage: api.Usage{InputTokens: 660747}}
		_, metadata := s.ToUIMessages()
		encoded, err := json.Marshal(metadata)
		Expect(err).NotTo(HaveOccurred())
		var projected map[string]any
		Expect(json.Unmarshal(encoded, &projected)).To(Succeed())
		Expect(projected).NotTo(HaveKey("contextTokens"))
		Expect(projected).To(HaveKeyWithValue("context", map[string]any{"usedTokens": float64(89595), "windowTokens": float64(258400), "freePercent": float64(69)}))
	})

	It("matches the native snapshot in full and incremental parsing, including compaction", func() {
		stream := strings.Join([]string{
			`{"type":"session_meta","payload":{"id":"example-session","cwd":"/work"}}`,
			`{"type":"event_msg","payload":{"type":"task_started","turn_id":"turn-1"}}`,
			`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":660747,"cached_input_tokens":582144,"output_tokens":2523,"total_tokens":663270},"last_token_usage":{"input_tokens":89350,"cached_input_tokens":88704,"output_tokens":245,"total_tokens":89595},"model_context_window":258400}}}`,
		}, "\n")
		uses, err := history.ExtractCodexToolUsesFromReader(strings.NewReader(stream))
		Expect(err).NotTo(HaveOccurred())
		full := buildCodexSession(uses, &history.CodexSessionInfo{ID: "example-session"})
		expected := &api.ContextUsage{UsedTokens: 89595, WindowTokens: 258400, FreePercent: 69}
		Expect(full.Context).To(Equal(expected))
		Expect(full.Turns).To(HaveLen(1))
		Expect(full.Turns[0].Context).To(Equal(expected))
		incremental := newCodexAccumulator("", true)
		for _, use := range uses {
			incremental.Add(nil, []history.ToolUse{use})
		}
		Expect(incremental.fullSession().Context).To(Equal(expected))
		compacted, err := history.ExtractCodexToolUsesFromReader(strings.NewReader(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":700000},"last_token_usage":{"total_tokens":0},"model_context_window":258400}}}`))
		Expect(err).NotTo(HaveOccurred())
		incremental.Add(nil, compacted)
		Expect(incremental.fullSession().Context).To(Equal(&api.ContextUsage{WindowTokens: 258400, FreePercent: 100}))
		encoded, err := json.Marshal(incremental.fullSession().Context)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(encoded)).To(MatchJSON(`{"usedTokens":0,"windowTokens":258400,"freePercent":100}`))
	})

	It("does not infer context from cumulative usage when the latest snapshot is missing", func() {
		uses, err := history.ExtractCodexToolUsesFromReader(strings.NewReader(`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":660747},"model_context_window":258400}}}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(buildCodexSession(uses, nil).Context).To(BeNil())
	})
})
