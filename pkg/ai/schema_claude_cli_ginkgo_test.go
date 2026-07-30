package ai

import (
	"encoding/json"
	"os"

	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Claude CLI schema shaping", func() {
	It("removes the unresolvable root dialect while preserving local references", func() {
		rejected, err := os.ReadFile("provider/testdata/claude_cli_rejected_schema.json")
		Expect(err).NotTo(HaveOccurred())

		got, err := SchemaJSONForBackend(api.BackendClaudeCLI, api.Prompt{SchemaJSON: rejected})
		Expect(err).NotTo(HaveOccurred())

		var root map[string]any
		Expect(json.Unmarshal(got, &root)).To(Succeed())
		Expect(root).NotTo(HaveKey("$schema"))
		Expect(root).To(HaveKeyWithValue("type", "object"))
		Expect(root).To(HaveKeyWithValue("$ref", "#/$defs/ResultEnvelope"))
		defs, ok := root["$defs"].(map[string]any)
		Expect(ok).To(BeTrue())
		resultEnvelope, ok := defs["ResultEnvelope"].(map[string]any)
		Expect(ok).To(BeTrue())
		properties, ok := resultEnvelope["properties"].(map[string]any)
		Expect(ok).To(BeTrue())
		questions, ok := properties["questions"].(map[string]any)
		Expect(ok).To(BeTrue())
		items, ok := questions["items"].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(items).To(HaveKeyWithValue("$ref", "#/$defs/AgentQuestion"))
	})

	It("keeps the Anthropic API dialect metadata", func() {
		rejected, err := os.ReadFile("provider/testdata/claude_cli_rejected_schema.json")
		Expect(err).NotTo(HaveOccurred())

		got, err := SchemaJSONForBackend(api.BackendAnthropic, api.Prompt{SchemaJSON: rejected})
		Expect(err).NotTo(HaveOccurred())

		var root map[string]any
		Expect(json.Unmarshal(got, &root)).To(Succeed())
		Expect(root).To(HaveKeyWithValue("$schema", "https://json-schema.org/draft/2020-12/schema"))
		Expect(root).NotTo(HaveKey("type"))

		claudeAgent, err := SchemaJSONForBackend(api.BackendClaudeAgent, api.Prompt{SchemaJSON: rejected})
		Expect(err).NotTo(HaveOccurred())
		Expect(claudeAgent).To(MatchJSON(got))

		openAICompatible, err := OpenAICompatibleSchema(rejected)
		Expect(err).NotTo(HaveOccurred())
		for _, backend := range []api.Backend{api.BackendCodexCLI, api.BackendCodexAgent} {
			codex, err := SchemaJSONForBackend(backend, api.Prompt{SchemaJSON: rejected})
			Expect(err).NotTo(HaveOccurred())
			Expect(codex).To(MatchJSON(openAICompatible))
		}

		for _, backend := range []api.Backend{api.BackendClaudeCmux, api.BackendCodexCmux} {
			cmux, err := SchemaJSONForBackend(backend, api.Prompt{SchemaJSON: rejected})
			Expect(err).NotTo(HaveOccurred())
			Expect(cmux).To(MatchJSON(rejected))
		}
	})

	It("fails loudly when a bare root reference has no resolvable type", func() {
		_, err := ClaudeCLICompatibleSchema(json.RawMessage(`{
			"$schema":"https://json-schema.org/draft/2020-12/schema",
			"$ref":"#/$defs/Missing",
			"$defs":{}
		}`))
		Expect(err).To(MatchError(ContainSubstring(`root $ref "#/$defs/Missing" must resolve`)))
	})
})
