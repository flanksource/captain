package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("admitted prompt runtime variants", func() {
	var path string
	var options AIPromptOptions

	BeforeEach(func() {
		dir := GinkgoT().TempDir()
		configPath := filepath.Join(dir, ".captain.yaml")
		Expect(os.WriteFile(configPath, []byte("ai:\n  providers:\n    anthropic:\n      mode: api\n      reasoningEffort: high\n"), 0o600)).To(Succeed())
		captainconfig.SetPathForTesting(configPath)
		DeferCleanup(captainconfig.SetPathForTesting, "")
		path = filepath.Join(dir, "review.prompt")
		Expect(os.WriteFile(path, []byte("---\nmodel: api:sonnet\n---\nReview the change."), 0o600)).To(Succeed())
		options = AIPromptOptions{MultiModels: []string{"agent:sonnet", "agent:opus"}}
		options.Fallback = []string{"sonnet-4-6"}
	})

	It("dispatches the complete fallback settings admitted by preview", func() {
		rendered, err := renderPromptCLI(context.Background(), path, options, "", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.Runtimes[0].Fallbacks).To(HaveExactElements(HaveField("Mode", api.ModeAPI)))
		variant := renderVariant(rendered, rendered.variants[0])
		Expect(variant.Input.Model).To(Equal(rendered.Runtimes[0]))
		Expect(variant.Config.Model).To(Equal(rendered.Runtimes[0]))
	})

	It("carries the admitted variant request and its authored provenance together", func() {
		rendered, err := renderPromptCLI(context.Background(), path, options, "", "")
		Expect(err).NotTo(HaveOccurred())
		variant := renderVariant(rendered, rendered.variants[1])
		Expect(variant.Resolution.Spec).To(Equal(variant.Input))
		Expect(variant.Resolution.Provenance["/model"].Source.Name).To(Equal("runtime 2"))
		Expect(variant.Resolution.Trace).To(ContainElement(SatisfyAll(HaveField("Name", "runtime 2"), HaveField("Spec.Model.Name", "agent:opus"))))
	})

	It("validates the executing variants after they repair the lower runtime mode", func() {
		Expect(os.WriteFile(path, []byte("---\nmodel: api:sonnet\nsandbox: docker\nruntimes:\n  - cli:sonnet\n  - cli:opus\n---\nReview the change."), 0o600)).To(Succeed())
		rendered, err := renderPromptCLI(context.Background(), path, AIPromptOptions{}, "", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.ValidationError).To(BeEmpty())
		Expect(rendered.Runtimes).To(HaveEach(HaveField("Mode", api.ModeCLI)))
	})

	It("expands an explicit wildcard through the shared runtime catalog before final composition", func() {
		options.MultiModels, options.Fallback = []string{"*:sol"}, nil
		rendered, err := renderPromptCLI(context.Background(), path, options, "", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.Runtimes).To(HaveLen(4))
		Expect(rendered.Runtimes).To(HaveEach(HaveField("Provider", api.OpenAI)))
	})

	It("fills mode-only variants from the lower authored model", func() {
		Expect(os.WriteFile(path, []byte("---\nmodel: sonnet\nmode: api\n---\nReview."), 0o600)).To(Succeed())
		options.MultiModels, options.Fallback = []string{"cli:", "cmux:"}, nil
		rendered, err := renderPromptCLI(context.Background(), path, options, "", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.Runtimes).To(HaveExactElements(
			SatisfyAll(HaveField("Name", "claude-sonnet-5"), HaveField("Mode", api.ModeCLI)),
			SatisfyAll(HaveField("Name", "claude-sonnet-5"), HaveField("Mode", api.ModeCmux)),
		))
	})

	It("accepts bare mode prefixes through the shared runtime grammar", func() {
		Expect(os.WriteFile(path, []byte("---\nmodel: sonnet\n---\nReview."), 0o600)).To(Succeed())
		options.MultiModels, options.Fallback = []string{"cli", "cmux"}, nil
		rendered, err := renderPromptCLI(context.Background(), path, options, "", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.Runtimes).To(HaveExactElements(HaveField("Mode", api.ModeCLI), HaveField("Mode", api.ModeCmux)))
	})

	It("deduplicates repeated CLI runtime selectors after final resolution", func() {
		options.MultiModels, options.Fallback = []string{"agent:sonnet", "agent:sonnet", "cli:sonnet"}, nil
		rendered, err := renderPromptCLI(context.Background(), path, options, "", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.Runtimes).To(HaveExactElements(HaveField("Mode", api.ModeAgent), HaveField("Mode", api.ModeCLI)))
	})

	It("leaves ordinary bare runtime selectors available for saved mode defaults", func() {
		Expect(os.WriteFile(path, []byte("---\nmodel: sonnet\n---\nReview."), 0o600)).To(Succeed())
		options.MultiModels, options.Fallback = []string{"sonnet", "opus"}, nil
		rendered, err := renderPromptCLI(context.Background(), path, options, "", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.Runtimes).To(HaveEach(HaveField("Mode", api.ModeAPI)))
		Expect(rendered.variants[0].Resolution.Trace).To(ContainElement(SatisfyAll(HaveField("Name", "runtime 1"), HaveField("Spec.Model.Name", "sonnet"))))
		Expect(rendered.variants[0].Resolution.Provenance["/mode"].Source.Kind).To(Equal(api.FieldSourceSaved))
	})

	It("carries prepared attachment refs while preserving variant source and model provenance", func() {
		options.Attach = []string{"diagram.png"}
		rendered, err := renderPromptCLI(context.Background(), path, options, "", "")
		Expect(err).NotTo(HaveOccurred())
		prepared := api.AttachmentRef{ID: api.AttachmentIDPrefix + strings.Repeat("a", 64), Filename: "diagram.png", MediaType: "image/png", Size: 512}
		rendered.Input.Prompt.Attachments = []api.AttachmentRef{prepared}
		variant := renderVariant(rendered, rendered.variants[1])
		Expect(variant.Input.Prompt.Attachments).To(Equal([]api.AttachmentRef{prepared}))
		Expect(variant.Resolution.Spec).To(Equal(variant.Input))
		Expect(variant.Resolution.Provenance["/model"].Source.Name).To(Equal("runtime 2"))
		Expect(variant.Resolution.Provenance["/prompt/attachments"].Source.Name).To(Equal("prompt flags"))
		Expect(variant.Resolution.Provenance["/prompt/attachments"].NormalizedBy).To(HaveField("Name", "attachment preparation"))
		Expect(rendered.variants[1].Request.Prompt.Attachments).To(Equal([]api.AttachmentRef{{Path: "diagram.png"}}))
		Expect(rendered.variants[1].Resolution.Provenance["/prompt/attachments"].NormalizedBy).To(BeNil())
	})
})
