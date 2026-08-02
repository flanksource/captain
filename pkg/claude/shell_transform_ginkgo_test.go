package claude

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/segmentio/encoding/json"
)

var _ = Describe("Claude Bash normalization", func() {
	It("transforms shell wrappers while extracting history tools", func() {
		uses := ExtractToolUses([]HistoryEntry{{
			Message: Message{
				Role: MessageRoleAssistant,
				Content: []ContentBlock{{
					Type:  ContentTypeToolUse,
					ID:    "tool-1",
					Name:  "Bash",
					Input: json.RawMessage(`{"command":"/bin/zsh -lc 'pnpm test'"}`),
				}},
			},
		}})

		Expect(uses).To(HaveLen(1))
		Expect(uses[0].Input).To(Equal(map[string]any{
			"command":    "pnpm test",
			"shell":      "zsh",
			"shellFlags": []string{"-l"},
		}))
	})
})
