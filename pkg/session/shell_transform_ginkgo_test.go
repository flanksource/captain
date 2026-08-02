package session

import (
	"strings"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/segmentio/encoding/json"
)

var _ = ginkgo.Describe("session Bash normalization", func() {
	ginkgo.It("stores transformed shell input in the canonical session part", func() {
		parts := partsFromEntry(claude.HistoryEntry{
			Message: claude.Message{
				Role: claude.MessageRoleAssistant,
				Content: []claude.ContentBlock{{
					Type:  claude.ContentTypeToolUse,
					ID:    "tool-1",
					Name:  "Bash",
					Input: json.RawMessage(`{"command":"/bin/zsh -lc 'pnpm test'"}`),
				}},
			},
		})

		Expect(parts).To(HaveLen(1))
		var input map[string]any
		Expect(json.Unmarshal(parts[0].Input, &input)).To(Succeed())
		Expect(input).To(Equal(map[string]any{
			"command":    "pnpm test",
			"shell":      "zsh",
			"shellFlags": []any{"-l"},
		}))
	})

	ginkgo.It("keeps raw source records out of canonical session JSON", func() {
		encoded, err := json.Marshal(Session{Messages: []Message{{
			Role:  "assistant",
			Parts: []Part{{Type: PartTool, ToolName: "Bash", Input: json.RawMessage(`{"command":"pnpm test","shell":"zsh","shellFlags":["-l"]}`)}},
			Raw:   json.RawMessage(`{"command":"/bin/zsh -lc 'pnpm test'"}`),
		}}})

		Expect(err).NotTo(HaveOccurred())
		Expect(string(encoded)).To(ContainSubstring(`"shell":"zsh"`))
		Expect(string(encoded)).NotTo(ContainSubstring(`"raw"`))
		Expect(strings.Contains(string(encoded), "/bin/zsh -lc")).To(BeFalse())
	})
})
