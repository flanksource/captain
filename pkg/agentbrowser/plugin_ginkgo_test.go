package agentbrowser

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The exact envelope agent-browser 0.31.1 writes to a plugin's stdin, captured
// from a real launch. Note it is a bare JSON object with no trailing newline: a
// line-oriented reader drops it entirely.
const launchMutateRequest = `{"capability":"launch.mutate","protocol":"agent-browser.plugin.v1",` +
	`"request":{"launchOptions":{"allowFileAccess":false,"args":[],"colorScheme":null,` +
	`"downloadPath":null,"engine":"chrome","extensions":null,"headless":true,` +
	`"hideScrollbars":true,"userAgent":null},"session":"captainprobe"},"type":"launch.mutate"}`

// writeBlockingFile puts a regular file where a directory is needed, so the
// record write fails for a reason captain cannot recover from.
func writeBlockingFile(path string) error {
	return os.WriteFile(path, []byte("not a directory"), 0o644)
}

func runPlugin(input, stateDir string) (browserPluginResponse, string, error) {
	var stdout, stderr bytes.Buffer
	err := RunBrowserPlugin(strings.NewReader(input), &stdout, &stderr, stateDir)
	var response browserPluginResponse
	if stdout.Len() > 0 {
		ExpectWithOffset(1, json.Unmarshal(stdout.Bytes(), &response)).To(Succeed())
	}
	return response, stderr.String(), err
}

var _ = Describe("agent-browser launch plugin", func() {
	Describe("plugin.manifest", func() {
		It("declares captain's name and its launch.mutate capability", func() {
			request := `{"protocol":"agent-browser.plugin.v1","type":"plugin.manifest",` +
				`"capability":"plugin.manifest","request":{}}`

			response, _, err := runPlugin(request, GinkgoT().TempDir())

			Expect(err).NotTo(HaveOccurred())
			Expect(response.Protocol).To(Equal("agent-browser.plugin.v1"))
			Expect(response.Success).To(BeTrue())
			Expect(response.Name).To(Equal("captain"))
			Expect(response.Capabilities).To(ConsistOf("launch.mutate"))
			Expect(response.Data.Name).To(Equal("captain"))
		})
	})

	Describe("launch.mutate", func() {
		It("records the link and returns a mutation-free success", func() {
			GinkgoT().Setenv("CLAUDE_CODE_SESSION_ID", "8c630de6-bdfc-41e6-8dd6-b6d999347dbe")
			GinkgoT().Setenv("CLAUDE_PID", "31766")
			GinkgoT().Setenv("AGENT_BROWSER_NAMESPACE", "")
			stateDir := GinkgoT().TempDir()

			response, stderrText, err := runPlugin(launchMutateRequest, stateDir)

			Expect(err).NotTo(HaveOccurred())
			Expect(stderrText).To(BeEmpty())
			Expect(response.Success).To(BeTrue())
			Expect(response.Protocol).To(Equal("agent-browser.plugin.v1"))

			record, err := readBrowserLinkRecord(browserLinkPath(stateDir, "", "captainprobe"))
			Expect(err).NotTo(HaveOccurred())
			Expect(record).NotTo(BeNil())
			Expect(record.BrowserSession).To(Equal("captainprobe"))
			Expect(record.AgentSource).To(Equal("claude"))
			Expect(record.AgentSessionID).To(Equal("8c630de6-bdfc-41e6-8dd6-b6d999347dbe"))
			Expect(record.ClaudePID).To(Equal(31766))
			Expect(record.LaunchedAt).NotTo(BeZero())
		})

		It("keeps the launch request verbatim, so agent-browser's own context survives", func() {
			GinkgoT().Setenv("CLAUDE_CODE_SESSION_ID", "session-1")
			stateDir := GinkgoT().TempDir()

			_, _, err := runPlugin(launchMutateRequest, stateDir)
			Expect(err).NotTo(HaveOccurred())

			record, err := readBrowserLinkRecord(browserLinkPath(stateDir, "", "captainprobe"))
			Expect(err).NotTo(HaveOccurred())
			var launch struct {
				LaunchOptions struct {
					Engine   string `json:"engine"`
					Headless bool   `json:"headless"`
				} `json:"launchOptions"`
			}
			Expect(json.Unmarshal(record.PluginRequest, &launch)).To(Succeed())
			Expect(launch.LaunchOptions.Engine).To(Equal("chrome"))
			Expect(launch.LaunchOptions.Headless).To(BeTrue())
		})

		It("files a namespaced launch under its namespace", func() {
			GinkgoT().Setenv("CLAUDE_CODE_SESSION_ID", "session-1")
			GinkgoT().Setenv("AGENT_BROWSER_NAMESPACE", "designpg")
			stateDir := GinkgoT().TempDir()

			_, _, err := runPlugin(launchMutateRequest, stateDir)

			Expect(err).NotTo(HaveOccurred())
			Expect(browserLinkPath(stateDir, "designpg", "captainprobe")).To(BeAnExistingFile())
		})

		It("warns and lets the launch proceed when no agent session can be identified", func() {
			GinkgoT().Setenv("CLAUDE_CODE_SESSION_ID", "")
			GinkgoT().Setenv("CODEX_THREAD_ID", "")
			stateDir := GinkgoT().TempDir()

			response, stderrText, err := runPlugin(launchMutateRequest, stateDir)

			Expect(err).NotTo(HaveOccurred())
			Expect(response.Success).To(BeTrue())
			Expect(stderrText).To(ContainSubstring("no agent session"))
			Expect(browserLinkPath(stateDir, "", "captainprobe")).NotTo(BeAnExistingFile())
		})

		It("fails loudly when the session was identified but the record cannot be written", func() {
			GinkgoT().Setenv("CLAUDE_CODE_SESSION_ID", "session-1")
			// A file where the state directory should be: the record cannot be stored.
			blocked := filepath.Join(GinkgoT().TempDir(), "state")
			Expect(writeBlockingFile(blocked)).To(Succeed())

			_, _, err := runPlugin(launchMutateRequest, blocked)

			Expect(err).To(HaveOccurred())
		})
	})

	Describe("protocol handling", func() {
		It("rejects an unsupported request type instead of silently accepting it", func() {
			request := `{"protocol":"agent-browser.plugin.v1","type":"credential.resolve",` +
				`"capability":"credential.read","request":{}}`

			response, _, err := runPlugin(request, GinkgoT().TempDir())

			Expect(err).NotTo(HaveOccurred())
			Expect(response.Success).To(BeFalse())
			Expect(response.Error).To(ContainSubstring("credential.resolve"))
		})

		It("rejects a request from an unknown protocol version", func() {
			request := `{"protocol":"agent-browser.plugin.v2","type":"launch.mutate",` +
				`"capability":"launch.mutate","request":{"session":"x"}}`

			response, _, err := runPlugin(request, GinkgoT().TempDir())

			Expect(err).NotTo(HaveOccurred())
			Expect(response.Success).To(BeFalse())
			Expect(response.Error).To(ContainSubstring("agent-browser.plugin.v2"))
		})

		It("returns cleanly on empty input", func() {
			_, _, err := runPlugin("", GinkgoT().TempDir())

			Expect(err).NotTo(HaveOccurred())
		})
	})
})
