package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("prompt render with schema hooks", func() {
	It("shows the schema the hooks emit, without running a model", func() {
		fixture, err := filepath.Abs("../ai/prompt/testdata/fixtures/jev-hooks.prompt")
		Expect(err).NotTo(HaveOccurred())
		rendered, err := renderPromptCLI(context.Background(), fixture, AIPromptOptions{Var: []string{"file=KEY-1.md", "issue=Annuity quote fails"}}, "", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.Input.Workflow.After[1].Prompt).To(Equal(filepath.Join(filepath.Dir(fixture), "jev.prompt")))

		Expect(previewSchemaHooks(context.Background(), &rendered)).To(Succeed())

		Expect(rendered.OutputSchemaType).To(BeEquivalentTo("typesafe-schema"))
		Expect(rendered.OutputSchema).To(HaveKeyWithValue("product", HaveKeyWithValue("criteria", HaveKey("group-life"))))
		Expect(rendered.Input.Prompt.TypeSafeQuestions).To(ContainSubstring(`"group-life"`))
	})
})

// inputHookPrompt writes a prompt whose input hook prints emit, and returns its path.
func inputHookPrompt(emit string) string {
	path := filepath.Join(GinkgoT().TempDir(), "classify.prompt")
	Expect(os.WriteFile(path, []byte(`---
model: deepseek-reasoner
mode: api
tools:
  load:
    description: Load the issue
    readonly: true
    exec: cat >/dev/null; printf '%s' '`+emit+`'
input:
  hooks:
    - tool: load
---
Classify {{issue}} from {{file}}.
`), 0o600)).To(Succeed())
	return path
}

var _ = Describe("prompt input hooks", func() {
	It("renders the body with the vars the hook returns", func() {
		path := inputHookPrompt(`{"vars":{"file":"KEY-1.md","issue":"the loaded issue"}}`)

		rendered, err := renderPromptCLI(context.Background(), path, AIPromptOptions{Var: []string{"file=KEY-1.md"}}, "", "")

		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.Skip).To(BeNil())
		Expect(rendered.Input.Prompt.User).To(Equal("Classify the loaded issue from KEY-1.md."))
		Expect(rendered.Input.Prompt.Variables).To(HaveKeyWithValue("issue", "the loaded issue"))
	})

	It("previews no schema hook for a render its input hooks skip", func() {
		dir := GinkgoT().TempDir()
		marker := filepath.Join(dir, "schema-hook-ran")
		path := filepath.Join(dir, "classify.prompt")
		Expect(os.WriteFile(path, []byte(`---
model: jev-latest
mode: api
tools:
  load:
    description: Load the issue
    readonly: true
    exec: cat >/dev/null; printf '%s' '{"skip":"no product classified yet"}'
  choices:
    description: Emit choices
    readonly: true
    exec: touch `+marker+`; exit 1
input:
  hooks:
    - tool: load
output:
  schemaType: typesafe-schema
  hooks:
    - tool: choices
  schema: {}
---
Classify {{issue}}.
`), 0o600)).To(Succeed())
		rendered, err := renderPromptCLI(context.Background(), path, AIPromptOptions{Var: []string{"file=KEY-1.md"}}, "", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.Skip).NotTo(BeNil())

		Expect(previewSchemaHooks(context.Background(), &rendered)).To(Succeed())

		Expect(marker).NotTo(BeAnExistingFile(), "a skipped render must not run its schema hooks")
	})

	It("skips the run before any model call, reporting status skipped", func() {
		withGinkgoCaptainDB()
		originalExecute := executePromptRequestFunc
		DeferCleanup(func() { executePromptRequestFunc = originalExecute })
		executePromptRequestFunc = func(context.Context, ai.Request, ai.Config, time.Duration, bool) (any, error) {
			Fail("a skipped run must not call the model")
			return nil, nil
		}

		result, err := RunPrompt(context.Background(), PromptInvocation{
			Reference: inputHookPrompt(`{"skip":"already classified"}`),
			Options:   AIPromptOptions{Var: []string{"file=KEY-1.md"}},
			Stdin:     strings.NewReader(""),
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(SatisfyAll(HaveField("Status", "skipped"), HaveField("SkipReason", "already classified")))
	})
})
