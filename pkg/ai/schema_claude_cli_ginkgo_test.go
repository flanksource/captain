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

		got, err := SchemaJSONForRuntime(api.Anthropic, api.ModeCLI, api.Prompt{SchemaJSON: rejected})
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

	// claude-agent reaches Claude Code through the Agent SDK's
	// `outputFormat: {type: "json_schema"}`, so it needs the same dialect strip
	// claude-cli gets. It was previously grouped with the Anthropic API, which
	// made every structured-output claude-agent run die on
	// `--json-schema is not a valid JSON Schema: no schema with key or ref ...`.
	It("strips the dialect for every backend that reaches Claude Code", func() {
		rejected, err := os.ReadFile("provider/testdata/claude_cli_rejected_schema.json")
		Expect(err).NotTo(HaveOccurred())

		cli, err := SchemaJSONForRuntime(api.Anthropic, api.ModeCLI, api.Prompt{SchemaJSON: rejected})
		Expect(err).NotTo(HaveOccurred())

		claudeAgent, err := SchemaJSONForRuntime(api.Anthropic, api.ModeAgent, api.Prompt{SchemaJSON: rejected})
		Expect(err).NotTo(HaveOccurred())
		Expect(claudeAgent).To(MatchJSON(cli))

		var agentRoot map[string]any
		Expect(json.Unmarshal(claudeAgent, &agentRoot)).To(Succeed())
		Expect(agentRoot).NotTo(HaveKey("$schema"))
		Expect(agentRoot).To(HaveKeyWithValue("type", "object"))
	})

	// The Anthropic API accepts the dialect declaration, so it must keep it —
	// this is the half of the old grouping that was correct.
	It("keeps the Anthropic API dialect metadata", func() {
		rejected, err := os.ReadFile("provider/testdata/claude_cli_rejected_schema.json")
		Expect(err).NotTo(HaveOccurred())

		got, err := SchemaJSONForRuntime(api.Anthropic, api.ModeAPI, api.Prompt{SchemaJSON: rejected})
		Expect(err).NotTo(HaveOccurred())

		var root map[string]any
		Expect(json.Unmarshal(got, &root)).To(Succeed())
		Expect(root).To(HaveKeyWithValue("$schema", "https://json-schema.org/draft/2020-12/schema"))
		Expect(root).NotTo(HaveKey("type"))

		openAICompatible, err := OpenAICompatibleSchema(rejected)
		Expect(err).NotTo(HaveOccurred())
		for _, mode := range []api.RuntimeMode{api.ModeCLI, api.ModeAgent} {
			codex, err := SchemaJSONForRuntime(api.OpenAI, mode, api.Prompt{SchemaJSON: rejected})
			Expect(err).NotTo(HaveOccurred())
			Expect(codex).To(MatchJSON(openAICompatible))
		}

		for _, provider := range []*api.ModelProvider{api.Anthropic, api.OpenAI} {
			cmux, err := SchemaJSONForRuntime(provider, api.ModeCmux, api.Prompt{SchemaJSON: rejected})
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
