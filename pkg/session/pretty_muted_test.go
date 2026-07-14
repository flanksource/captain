package session

import (
	"testing"

	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestPrettyMuted(t *testing.T) {
	RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "Session Pretty Muted Suite")
}

var _ = ginkgo.Describe("session Pretty muted styles", func() {
	ginkgo.It("renders summary values and assistant payloads without low-contrast palette classes", func() {
		session := &Session{
			ID:      "session-id",
			Project: "tenant-x",
			Messages: []Message{{
				Role:  "assistant",
				Parts: []Part{{Type: PartText, Text: "assistant payload"}},
			}},
		}

		rendered := session.Pretty()
		Expect(rendered.HTML()).NotTo(MatchRegexp(`text-gray-(600|700)`))
		Expect(rendered.HTML()).To(ContainSubstring("text-muted"))
		Expect(rendered.String()).To(ContainSubstring("tenant-x"))
		Expect(rendered.String()).To(ContainSubstring("assistant payload"))
	})
})
