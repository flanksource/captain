package provider

import (
	"encoding/json"
	"os"
	"slices"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Claude CLI structured output", func() {
	It("passes the accepted schema root to --json-schema", func() {
		rejected, err := os.ReadFile("testdata/claude_cli_rejected_schema.json")
		Expect(err).NotTo(HaveOccurred())

		args, cleanup, err := buildClaudeCLIArgs("sonnet", ai.Request{
			Prompt: api.Prompt{SchemaJSON: rejected},
		})
		DeferCleanup(cleanup)
		Expect(err).NotTo(HaveOccurred())

		index := slices.Index(args, "--json-schema")
		Expect(index).To(BeNumerically(">=", 0))
		Expect(index + 1).To(BeNumerically("<", len(args)))

		var root map[string]any
		Expect(json.Unmarshal([]byte(args[index+1]), &root)).To(Succeed())
		Expect(root).NotTo(HaveKey("$schema"))
		Expect(root).To(HaveKeyWithValue("type", "object"))
		Expect(root).To(HaveKeyWithValue("$ref", "#/$defs/ResultEnvelope"))
		Expect(root).To(HaveKey("$defs"))
	})
})
