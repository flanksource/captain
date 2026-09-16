package session

import (
	"strings"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = ginkgo.Describe("Codex file changes", func() {
	ginkgo.It("classifies an attached image as read and emits a canonical file part", func() {
		const digest = "7d432b84dfb5e1cda66c73adae2848da8f2afea3f6f1bd255f517ebce71b3d8e"
		const imagePath = "/repo/.captain/attachments/sha256/7d/" + digest
		stream := strings.Join([]string{
			`{"timestamp":"2026-09-11T11:55:21Z","type":"session_meta","payload":{"id":"session-image","cwd":"/repo"}}`,
			`{"timestamp":"2026-09-11T11:55:22Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<image name=[Image #1] path=\"` + imagePath + `\">"},{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgo="},{"type":"input_text","text":"</image>"},{"type":"input_text","text":"Classify the attached report"}]}}`,
		}, "\n")

		uses, err := history.ExtractCodexToolUsesFromReader(strings.NewReader(stream))
		Expect(err).NotTo(HaveOccurred())

		built := buildCodexSession(uses, &history.CodexSessionInfo{ID: "session-image", CWD: "/repo"})

		Expect(built.Files.Read).To(Equal([]string{".captain/attachments/sha256/7d/" + digest}))
		Expect(built.Messages).To(HaveLen(1))
		Expect(built.Messages[0].Parts).To(Equal([]Part{
			{
				Type: PartFile, MediaType: "image/png", URL: "/api/attachments/sha256:" + digest,
				Filename: "Image #1", AttachmentID: "sha256:" + digest,
			},
			{Type: PartText, Text: "Classify the attached report"},
		}))
	})

	ginkgo.It("collects native apply_patch write paths", func() {
		stream := strings.Join([]string{
			`{"timestamp":"2026-07-26T17:00:00Z","type":"session_meta","payload":{"id":"patch-session","cwd":"/repo"}}`,
			`{"timestamp":"2026-07-26T17:00:01Z","type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","call_id":"patch-1","input":"*** Begin Patch\n*** Update File: /repo/pkg/existing.go\n*** Move to: /repo/pkg/moved.go\n*** Add File: /repo/pkg/added.go\n+content := \"*** Delete File: /repo/pkg/ignored.go\"\n*** Delete File: /repo/pkg/deleted.go\n*** Update File: /repo/pkg/existing.go\n*** End Patch"}}`,
			`{"timestamp":"2026-07-26T17:00:02Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"patch-1","output":"Success"}}`,
		}, "\n")

		uses, err := history.ExtractCodexToolUsesFromReader(strings.NewReader(stream))
		Expect(err).NotTo(HaveOccurred())

		built := buildCodexSession(uses, &history.CodexSessionInfo{ID: "patch-session", CWD: "/repo"})

		Expect(built.Files.Written).To(Equal([]string{
			"pkg/added.go",
			"pkg/deleted.go",
			"pkg/existing.go",
			"pkg/moved.go",
		}))
	})

	ginkgo.It("collects paths from JavaScript-wrapped exec commands", func() {
		stream := strings.Join([]string{
			`{"timestamp":"2026-07-26T17:00:00Z","type":"session_meta","payload":{"id":"exec-session","cwd":"/repo"}}`,
			`{"timestamp":"2026-07-26T17:00:01Z","type":"response_item","payload":{"type":"custom_tool_call","name":"exec","call_id":"exec-1","input":"const r = await tools.exec_command({cmd: \"printf output > generated.txt\", workdir: \"/repo/out\"}); text(r.output);"}}`,
			`{"timestamp":"2026-07-26T17:00:02Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"exec-1","output":"Success"}}`,
		}, "\n")

		uses, err := history.ExtractCodexToolUsesFromReader(strings.NewReader(stream))
		Expect(err).NotTo(HaveOccurred())

		built := buildCodexSession(uses, &history.CodexSessionInfo{ID: "exec-session", CWD: "/repo"})

		Expect(built.Files.Written).To(Equal([]string{"out/generated.txt"}))
	})
})
