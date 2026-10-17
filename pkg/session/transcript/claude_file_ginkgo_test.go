package transcript_test

import (
	"os"
	"path/filepath"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/captain/pkg/session/transcript"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const claudeFixtureSession = "11111111-1111-1111-1111-111111111111"

// writeClaudeFixture writes one root transcript and its sub-agent transcript
// under projectsDir/<project>, the layout Claude Code records.
func writeClaudeFixture(projectsDir, project string) string {
	GinkgoHelper()
	projectDir := filepath.Join(projectsDir, project)
	agentDir := filepath.Join(projectDir, claudeFixtureSession, "subagents")
	Expect(os.MkdirAll(agentDir, 0o755)).To(Succeed())
	root := filepath.Join(projectDir, claudeFixtureSession+".jsonl")
	Expect(os.WriteFile(root, []byte(
		`{"type":"user","sessionId":"`+claudeFixtureSession+`","uuid":"u1","timestamp":"2026-10-16T10:00:00Z","message":{"role":"user","content":"list the files"}}`+"\n"+
			`{"type":"assistant","sessionId":"`+claudeFixtureSession+`","uuid":"m1","parentUuid":"u1","timestamp":"2026-10-16T10:00:05Z","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"tool_use","id":"t1","name":"Agent","input":{"subagent_type":"general-purpose","description":"list files"}}]}}`+"\n",
	), 0o644)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(agentDir, "agent-abc.jsonl"), []byte(
		`{"type":"assistant","sessionId":"`+claudeFixtureSession+`","uuid":"a1","timestamp":"2026-10-16T10:00:10Z","isSidechain":true,"agentId":"abc","message":{"role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"README.md"}]}}`+"\n",
	), 0o644)).To(Succeed())
	Expect(os.WriteFile(filepath.Join(agentDir, "agent-abc.meta.json"), []byte(
		`{"agentType":"general-purpose","description":"list files","toolUseId":"t1","spawnDepth":1}`,
	), 0o644)).To(Succeed())
	return root
}

var _ = Describe("Parse claude transcript", func() {
	var projectsDir string

	BeforeEach(func() {
		home := GinkgoT().TempDir()
		GinkgoT().Setenv("HOME", home)
		projectsDir = claude.GetProjectsDir()
	})

	It("builds the same session the history-wide discovery builds", func() {
		root := writeClaudeFixture(projectsDir, "-work-project")
		discovered, err := session.Build("", true, claude.Filter{
			SessionIDs: []string{claudeFixtureSession}, KeepRaw: true, IncludeAgents: true,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(discovered).To(HaveLen(1))

		parsed, err := transcript.Parse(transcript.Candidate{ID: claudeFixtureSession, Source: "claude", Path: root})

		Expect(err).NotTo(HaveOccurred())
		Expect(parsed).To(Equal(discovered[0]))
		Expect(parsed.Agents).To(HaveLen(2), "the root plus the sub-agent beside its transcript")
	})

	It("reads only the transcript the candidate names", func() {
		root := writeClaudeFixture(projectsDir, "-work-project")
		alone, err := transcript.Parse(transcript.Candidate{ID: claudeFixtureSession, Source: "claude", Path: root})
		Expect(err).NotTo(HaveOccurred())
		// A copy under another project shares the id; history-wide discovery
		// merges it into the session, so only a path-scoped read ignores it.
		writeClaudeFixture(projectsDir, "-work-other-project")

		parsed, err := transcript.Parse(transcript.Candidate{ID: claudeFixtureSession, Source: "claude", Path: root})

		Expect(err).NotTo(HaveOccurred())
		Expect(parsed.Messages).To(HaveLen(len(alone.Messages)))
		Expect(parsed.Agents).To(HaveLen(2))
	})

	It("reports a missing transcript as not existing", func() {
		missing := filepath.Join(projectsDir, "-work-project", claudeFixtureSession+".jsonl")

		_, err := transcript.Parse(transcript.Candidate{ID: claudeFixtureSession, Source: "claude", Path: missing})

		Expect(err).To(MatchError(os.ErrNotExist))
	})
})
