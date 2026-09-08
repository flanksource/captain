package transcript_test

import (
	"path/filepath"

	"github.com/flanksource/captain/pkg/session/transcript"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Parse", func() {
	It("rejects an unsupported transcript source", func() {
		_, err := transcript.Parse(transcript.Candidate{
			ID:     "provider-session",
			Source: "captain",
			Path:   filepath.Join("sessions", "provider-session.jsonl"),
		})

		Expect(err).To(MatchError(`unknown session source "captain"`))
	})

	It("requires a transcript path", func() {
		_, err := transcript.Parse(transcript.Candidate{ID: "provider-session", Source: "codex"})

		Expect(err).To(MatchError("transcript path is required"))
	})
})
