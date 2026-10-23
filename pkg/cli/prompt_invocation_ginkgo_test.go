package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/runtimeprofiles"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Prompt invocation", func() {
	var ctx context.Context

	BeforeEach(func() {
		captainconfig.SetPathForTesting(filepath.Join(GinkgoT().TempDir(), "captain.yaml"))
		DeferCleanup(func() { captainconfig.SetPathForTesting("") })
		catalog, err := runtimeprofiles.NewCatalog(runtimeprofiles.NewBuiltinSource())
		Expect(err).NotTo(HaveOccurred())
		ctx = ContextWithRuntimeCatalog(context.Background(), catalog)
	})

	It("applies the lint preset after authored output schemas for every runtime", func() {
		path := filepath.Join(GinkgoT().TempDir(), "review.prompt")
		Expect(os.WriteFile(path, []byte("---\nmodel: api:sonnet\nruntimes: [api:sonnet, api:haiku]\noutput:\n  schema:\n    old: string\n---\nReview {{change}}"), 0o600)).To(Succeed())
		rendered, err := RenderPrompt(ctx, PromptInvocation{Reference: path, Stdin: strings.NewReader("diff"), Options: AIPromptOptions{
			RequiredPresets: []string{"lint"}, Var: []string{"change=-"},
		}})
		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.ValidationError).To(BeEmpty())
		Expect(rendered.Input.Prompt.User).To(Equal("Review diff"))
		Expect(rendered.variants).To(HaveLen(2))
		for _, variant := range rendered.variants {
			Expect(variant.Request.Prompt.SchemaJSON).To(ContainSubstring(`"violations"`))
			Expect(variant.Request.Prompt.SchemaJSON).NotTo(ContainSubstring(`"old"`))
			Expect(variant.Request.Prompt.SchemaStrictness).To(Equal(api.SchemaStrictnessError))
			Expect(variant.Resolution.Trace[len(variant.Resolution.Trace)-1].Name).To(Equal("Lint"))
		}
	})

	DescribeTable("supports Captain input sources",
		func(reference, prompt, stdin, expected string) {
			rendered, err := RenderPrompt(ctx, PromptInvocation{Reference: reference, Stdin: strings.NewReader(stdin), Options: AIPromptOptions{
				Prompt: prompt, RequiredPresets: []string{"lint"},
			}})
			Expect(err).NotTo(HaveOccurred())
			Expect(rendered.Input.Prompt.User).To(Equal(expected))
			Expect(rendered.Input.Prompt.SchemaJSON).To(ContainSubstring(`"violations"`))
		},
		Entry("inline", "", "Review the change", "", "Review the change"),
		Entry("stdin", "-", "", "Review piped content", "Review piped content"),
	)

	It("replaces TypeSafe output questions while preserving the input schema", func() {
		path := filepath.Join(GinkgoT().TempDir(), "review.prompt")
		Expect(os.WriteFile(path, []byte(`---
model: api:sonnet
input:
  schemaType: json-schema
  schema:
    type: object
    required: [change]
    properties:
      change:
        type: string
output:
  schemaType: typesafe-schema
  schema:
    valid:
      type: noul
      instructions: Is this change valid?
---
Review {{change}}`), 0o600)).To(Succeed())
		rendered, err := RenderPrompt(ctx, PromptInvocation{Reference: path, Stdin: strings.NewReader("diff"), Options: AIPromptOptions{
			RequiredPresets: []string{"lint"}, Var: []string{"change=-"}, NoPresets: true,
		}})
		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.ValidationError).To(BeEmpty())
		Expect(rendered.Input.Prompt.InputSchemaJSON).To(ContainSubstring(`"change"`))
		Expect(rendered.Input.Prompt.TypeSafeQuestions).To(BeEmpty())
		Expect(rendered.Input.Prompt.OutputSchemaType).To(Equal(api.SchemaTypeJSONSchema))
		Expect(rendered.Input.Prompt.SchemaJSON).To(ContainSubstring(`"violations"`))
	})
})
