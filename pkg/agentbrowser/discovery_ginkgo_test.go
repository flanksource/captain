package agentbrowser

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// writeSidecars lays out one agent-browser session's sidecar files, mirroring
// what its daemon writes next to the socket.
func writeSidecars(dir, session string, files map[string]string) {
	ExpectWithOffset(1, os.MkdirAll(dir, 0o755)).To(Succeed())
	for extension, content := range files {
		path := filepath.Join(dir, session+"."+extension)
		ExpectWithOffset(1, os.WriteFile(path, []byte(content), 0o644)).To(Succeed())
	}
}

var _ = Describe("agent-browser sidecar discovery", func() {
	Describe("agentBrowserSocketDir", func() {
		It("prefers an explicit socket dir over every other source", func() {
			GinkgoT().Setenv("AGENT_BROWSER_SOCKET_DIR", "/custom/sockets")
			GinkgoT().Setenv("XDG_RUNTIME_DIR", "/run/user/501")

			Expect(agentBrowserSocketDir()).To(Equal("/custom/sockets"))
		})

		It("falls back to an agent-browser subdirectory of the XDG runtime dir", func() {
			GinkgoT().Setenv("AGENT_BROWSER_SOCKET_DIR", "")
			GinkgoT().Setenv("XDG_RUNTIME_DIR", "/run/user/501")

			Expect(agentBrowserSocketDir()).To(Equal("/run/user/501/agent-browser"))
		})

		It("falls back to ~/.agent-browser", func() {
			GinkgoT().Setenv("AGENT_BROWSER_SOCKET_DIR", "")
			GinkgoT().Setenv("XDG_RUNTIME_DIR", "")
			home, err := os.UserHomeDir()
			Expect(err).NotTo(HaveOccurred())

			Expect(agentBrowserSocketDir()).To(Equal(filepath.Join(home, ".agent-browser")))
		})
	})

	Describe("scanBrowserSidecars", func() {
		var socketDir string

		BeforeEach(func() {
			socketDir = GinkgoT().TempDir()

			writeSidecars(socketDir, "default", map[string]string{
				"pid": "11473\n", "sock": "", "version": "0.34.0\n",
				"engine": "chrome\n", "stream": "54983\n", "config": "f5b4ade1dfc51c5e\n",
			})
			// A daemon that died without cleaning up: pid file, no socket.
			writeSidecars(socketDir, "stale", map[string]string{"pid": "999001\n"})
			// The 1281 orphaned .config files on a real machine must not become rows.
			writeSidecars(socketDir, "orphan", map[string]string{"config": "f5b4ade1dfc51c5e\n"})
			// The dashboard server is not a session.
			writeSidecars(socketDir, "dashboard", map[string]string{"pid": "68854\n"})

			writeSidecars(filepath.Join(socketDir, "namespaces", "designpg", "run"), "designpg",
				map[string]string{"pid": "9921\n", "sock": "", "engine": "chrome\n"})
		})

		It("lists sessions from the root and from every namespace", func() {
			sidecars, err := scanBrowserSidecars(socketDir)

			Expect(err).NotTo(HaveOccurred())
			names := make([]string, len(sidecars))
			for i, sidecar := range sidecars {
				names[i] = sidecar.Session
			}
			Expect(names).To(ConsistOf("default", "stale", "designpg"))
		})

		It("reads the companion sidecars of a live session", func() {
			sidecars, err := scanBrowserSidecars(socketDir)
			Expect(err).NotTo(HaveOccurred())

			Expect(sidecars).To(ContainElement(browserSidecar{
				Session: "default", Namespace: "", Dir: socketDir,
				DaemonPID:  11473,
				SocketPath: filepath.Join(socketDir, "default.sock"),
				Version:    "0.34.0", Engine: "chrome", StreamPort: 54983,
				ConfigHash: "f5b4ade1dfc51c5e",
			}))
		})

		It("labels a namespaced session with its namespace", func() {
			sidecars, err := scanBrowserSidecars(socketDir)
			Expect(err).NotTo(HaveOccurred())

			for _, sidecar := range sidecars {
				if sidecar.Session == "designpg" {
					Expect(sidecar.Namespace).To(Equal("designpg"))
					Expect(sidecar.DaemonPID).To(Equal(9921))
					return
				}
			}
			Fail("namespaced session was not discovered")
		})

		It("keeps a session whose daemon is gone, so the row can render as stale", func() {
			sidecars, err := scanBrowserSidecars(socketDir)
			Expect(err).NotTo(HaveOccurred())

			for _, sidecar := range sidecars {
				if sidecar.Session == "stale" {
					Expect(sidecar.DaemonPID).To(Equal(999001))
					Expect(sidecar.SocketPath).To(BeEmpty())
					return
				}
			}
			Fail("stale session was not discovered")
		})

		It("returns nothing for a socket dir that does not exist", func() {
			sidecars, err := scanBrowserSidecars(filepath.Join(socketDir, "missing"))

			Expect(err).NotTo(HaveOccurred())
			Expect(sidecars).To(BeEmpty())
		})
	})
})
