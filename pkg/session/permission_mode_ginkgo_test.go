package session

import (
	"os"
	"path/filepath"

	"github.com/flanksource/captain/pkg/api"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// permissionModeTranscript switches posture twice; Claude Code rewrites the
// permission-mode checkpoint (with no uuid or timestamp) whenever it changes.
const permissionModeTranscript = `{"type":"permission-mode","permissionMode":"plan","sessionId":"0195c1de-4ab8-7000-8000-0123456789ab"}
{"type":"user","uuid":"u-1","sessionId":"0195c1de-4ab8-7000-8000-0123456789ab","timestamp":"2026-07-05T10:00:00Z","cwd":"/home/dev/example","message":{"role":"user","content":"hello"}}
{"type":"assistant","uuid":"a-1","sessionId":"0195c1de-4ab8-7000-8000-0123456789ab","timestamp":"2026-07-05T10:00:05Z","message":{"role":"assistant","model":"claude-sonnet-5","usage":{"input_tokens":100,"output_tokens":20},"content":[{"type":"text","text":"hi there"}]}}
{"type":"permission-mode","permissionMode":"auto","sessionId":"0195c1de-4ab8-7000-8000-0123456789ab"}
`

var _ = ginkgo.Describe("transcript permission mode", func() {
	build := func(transcript string) *Session {
		dir := filepath.Join(ginkgo.GinkgoT().TempDir(), "projects", "-home-dev-example")
		Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
		path := filepath.Join(dir, "0195c1de-4ab8-7000-8000-0123456789ab.jsonl")
		Expect(os.WriteFile(path, []byte(transcript), 0o644)).To(Succeed())
		s, _, err := BuildTranscriptFile(path)
		Expect(err).NotTo(HaveOccurred())
		return s
	}

	ginkgo.It("reports the last posture the transcript recorded", func() {
		Expect(build(permissionModeTranscript).PermissionMode).To(Equal(api.PermissionAuto))
	})

	ginkgo.It("consumes the checkpoint rather than listing it as a session event", func() {
		for _, event := range build(permissionModeTranscript).Events {
			Expect(event.Type).NotTo(Equal("permission-mode"))
		}
	})

	ginkgo.It("reports no posture for a transcript that never recorded one", func() {
		Expect(build(transcriptFixture).PermissionMode).To(BeEmpty())
	})
})
