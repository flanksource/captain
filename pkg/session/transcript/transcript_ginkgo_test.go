package transcript_test

import (
	"os"
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

	It("reads a Claude transcript stored outside Claude's projects directory, with its sub-agents", func() {
		GinkgoT().Setenv("HOME", GinkgoT().TempDir())
		id := "b0ac69b8-27e7-4347-b708-1062db8ee0d9"
		dir := filepath.Join(GinkgoT().TempDir(), "claude", "-workspace-src")
		root := filepath.Join(dir, id+".jsonl")
		writeLines(root,
			`{"type":"user","uuid":"u1","sessionId":"`+id+`","timestamp":"2026-10-19T05:52:23Z","cwd":"/workspace/src","message":{"role":"user","content":"Describe the README"}}`,
			`{"type":"assistant","uuid":"a1","parentUuid":"u1","sessionId":"`+id+`","timestamp":"2026-10-19T05:52:30Z","cwd":"/workspace/src","message":{"role":"assistant","content":[{"type":"text","text":"It covers setup."}]}}`,
		)
		writeLines(filepath.Join(dir, id, "subagents", "agent-explore.jsonl"),
			`{"type":"assistant","uuid":"x1","parentUuid":"a1","sessionId":"`+id+`","timestamp":"2026-10-19T05:52:31Z","cwd":"/workspace/src","message":{"role":"assistant","content":[{"type":"text","text":"Explored."}]}}`,
		)

		parsed, err := transcript.Parse(transcript.Candidate{ID: id, Source: "claude", Path: root})

		Expect(err).NotTo(HaveOccurred())
		Expect(parsed.ID).To(Equal(id))
		Expect(parsed.InitialPrompt).To(Equal("Describe the README"))
		var agents []string
		for _, agent := range parsed.Agents {
			agents = append(agents, agent.ID)
		}
		Expect(agents).To(ContainElement("explore"))
	})
})

func writeLines(path string, lines ...string) {
	Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
	content := ""
	for _, line := range lines {
		content += line + "\n"
	}
	Expect(os.WriteFile(path, []byte(content), 0o600)).To(Succeed())
}
