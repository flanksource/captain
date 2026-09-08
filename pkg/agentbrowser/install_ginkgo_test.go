package agentbrowser

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func decodeConfig(document []byte) map[string]any {
	var decoded map[string]any
	ExpectWithOffset(1, json.Unmarshal(document, &decoded)).To(Succeed())
	return decoded
}

var _ = Describe("agent-browser plugin installation", func() {
	entry := agentBrowserPluginEntry("/usr/local/bin/captain")

	It("creates the plugin list in an empty configuration", func() {
		merged, changed, err := mergeAgentBrowserPlugin(nil, entry)

		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue())
		plugins := decodeConfig(merged)["plugins"].([]any)
		Expect(plugins).To(HaveLen(1))
		Expect(plugins[0].(map[string]any)["name"]).To(Equal("captain"))
		Expect(plugins[0].(map[string]any)["command"]).To(Equal("/usr/local/bin/captain"))
		Expect(plugins[0].(map[string]any)["args"]).To(Equal([]any{"browser", "plugin"}))
		Expect(plugins[0].(map[string]any)["capabilities"]).To(Equal([]any{"launch.mutate"}))
	})

	It("preserves unrelated settings and other plugins", func() {
		existing := []byte(`{"headed":true,"proxy":"http://localhost:8080",` +
			`"plugins":[{"name":"stealth","command":"agent-browser-plugin-stealth","capabilities":["launch.mutate"]}]}`)

		merged, changed, err := mergeAgentBrowserPlugin(existing, entry)

		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue())
		config := decodeConfig(merged)
		Expect(config["headed"]).To(BeTrue())
		Expect(config["proxy"]).To(Equal("http://localhost:8080"))
		plugins := config["plugins"].([]any)
		Expect(plugins).To(HaveLen(2))
		Expect(plugins[0].(map[string]any)["name"]).To(Equal("stealth"))
		Expect(plugins[1].(map[string]any)["name"]).To(Equal("captain"))
	})

	It("is idempotent", func() {
		first, _, err := mergeAgentBrowserPlugin(nil, entry)
		Expect(err).NotTo(HaveOccurred())

		second, changed, err := mergeAgentBrowserPlugin(first, entry)

		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeFalse())
		Expect(second).To(MatchJSON(first))
	})

	It("repoints an entry left behind by a captain binary that has moved", func() {
		existing := []byte(`{"plugins":[{"name":"captain","command":"/old/captain","capabilities":["launch.mutate"]}]}`)

		merged, changed, err := mergeAgentBrowserPlugin(existing, entry)

		Expect(err).NotTo(HaveOccurred())
		Expect(changed).To(BeTrue())
		plugins := decodeConfig(merged)["plugins"].([]any)
		Expect(plugins).To(HaveLen(1))
		Expect(plugins[0].(map[string]any)["command"]).To(Equal("/usr/local/bin/captain"))
	})

	It("refuses to rewrite a configuration it cannot parse", func() {
		_, _, err := mergeAgentBrowserPlugin([]byte("{not json"), entry)

		Expect(err).To(HaveOccurred())
	})

	It("refuses a plugins key that is not a list", func() {
		_, _, err := mergeAgentBrowserPlugin([]byte(`{"plugins":"stealth"}`), entry)

		Expect(err).To(MatchError(ContainSubstring("plugins")))
	})
})
