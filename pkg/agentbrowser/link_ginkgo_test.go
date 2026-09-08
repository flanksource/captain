package agentbrowser

import (
	"os"
	"path/filepath"
	"time"

	clickyprocess "github.com/flanksource/clicky/process"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("browser session linking", func() {
	sidecar := browserSidecar{Session: "default", DaemonPID: 9000}

	// A daemon launched from a claude agent's shell, with chrome underneath it.
	nestedTree := func() *clickyprocess.Snapshot {
		return clickyprocess.NewSnapshot([]clickyprocess.Process{
			{PID: 9100, PPID: 1, Command: "claude --session-id abc123-session"},
			{PID: 9000, PPID: 9100, Command: "agent-browser --daemon default"},
			{PID: 9001, PPID: 9000, Command: "Google Chrome for Testing"},
		})
	}

	Describe("resolveBrowserLink", func() {
		It("prefers the daemon's inherited claude session id", func() {
			link := resolveBrowserLink(sidecar, BrowserLinkInputs{
				Tree: nestedTree(),
				Env: map[string]string{
					"CLAUDE_CODE_SESSION_ID": "8c630de6-bdfc-41e6-8dd6-b6d999347dbe",
					"CLAUDE_PID":             "33745",
					"CMUX_SURFACE_ID":        "SURFACE-1",
				},
			})

			Expect(link.Source).To(Equal(BrowserLinkEnv))
			Expect(link.AgentSource).To(Equal("claude"))
			Expect(link.AgentSessionID).To(Equal("8c630de6-bdfc-41e6-8dd6-b6d999347dbe"))
			Expect(link.AgentPID).To(Equal(33745))
			Expect(link.Surface).NotTo(BeNil())
			Expect(link.Surface.SurfaceID).To(Equal("SURFACE-1"))
		})

		It("recognises a codex thread id", func() {
			link := resolveBrowserLink(sidecar, BrowserLinkInputs{
				Env: map[string]string{"CODEX_THREAD_ID": "01f2a9c0-0000-7000-8000-000000000000"},
			})

			Expect(link.Source).To(Equal(BrowserLinkEnv))
			Expect(link.AgentSource).To(Equal("codex"))
			Expect(link.AgentSessionID).To(Equal("01f2a9c0-0000-7000-8000-000000000000"))
		})

		It("falls back to the plugin-written record when the environment is unreadable", func() {
			link := resolveBrowserLink(sidecar, BrowserLinkInputs{
				Tree: nestedTree(),
				Record: &browserLinkRecord{
					AgentSource: "claude", AgentSessionID: "recorded-session",
					CWD: "/repo", ClaudePID: 4242,
				},
			})

			Expect(link.Source).To(Equal(BrowserLinkSidecar))
			Expect(link.AgentSessionID).To(Equal("recorded-session"))
			Expect(link.CWD).To(Equal("/repo"))
			Expect(link.AgentPID).To(Equal(4242))
		})

		It("walks the process ancestry when neither the environment nor a record identifies the agent", func() {
			link := resolveBrowserLink(sidecar, BrowserLinkInputs{Tree: nestedTree()})

			Expect(link.Source).To(Equal(BrowserLinkAncestor))
			Expect(link.AgentSource).To(Equal("claude"))
			Expect(link.AgentSessionID).To(Equal("abc123-session"))
			Expect(link.AgentPID).To(Equal(9100))
		})

		It("reports no link rather than guessing when nothing identifies the agent", func() {
			orphan := clickyprocess.NewSnapshot([]clickyprocess.Process{
				{PID: 9000, PPID: 1, Command: "agent-browser --daemon default"},
			})

			link := resolveBrowserLink(sidecar, BrowserLinkInputs{Tree: orphan})

			Expect(link.Source).To(Equal(BrowserLinkNone))
			Expect(link.AgentSessionID).To(BeEmpty())
			Expect(link.AgentSource).To(BeEmpty())
		})
	})

	Describe("the plugin-written record", func() {
		It("round-trips through the captain browser state directory", func() {
			root := GinkgoT().TempDir()
			written := browserLinkRecord{
				BrowserSession: "captain-fb7c9f910548", Namespace: "designpg",
				SocketDir: "/Users/moshe/.agent-browser", DaemonPID: 11473,
				AgentSource: "claude", AgentSessionID: "8c630de6", ClaudePID: 33745,
				CWD: "/repo", LaunchedAt: time.Date(2026, time.September, 7, 9, 14, 2, 0, time.UTC),
			}

			path := browserLinkPath(root, "designpg", "captain-fb7c9f910548")
			Expect(writeBrowserLinkRecord(path, written)).To(Succeed())
			Expect(path).To(Equal(filepath.Join(root, "designpg", "captain-fb7c9f910548.json")))

			Expect(readBrowserLinkRecord(path)).To(Equal(&written))
		})

		It("files an unnamespaced session under a stable placeholder directory", func() {
			Expect(browserLinkPath("/state", "", "default")).
				To(Equal(filepath.Join("/state", "_", "default.json")))
		})

		It("returns no record when the file is absent", func() {
			record, err := readBrowserLinkRecord(filepath.Join(GinkgoT().TempDir(), "missing.json"))

			Expect(err).NotTo(HaveOccurred())
			Expect(record).To(BeNil())
		})

		It("fails loudly on a corrupt record rather than silently dropping the link", func() {
			path := filepath.Join(GinkgoT().TempDir(), "broken.json")
			Expect(os.WriteFile(path, []byte("{not json"), 0o644)).To(Succeed())

			_, err := readBrowserLinkRecord(path)

			Expect(err).To(HaveOccurred())
		})
	})
})
