package prompt

import (
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Prompt validation", func() {
	DescribeTable("reports invalid declarations without executing a prompt",
		func(source, rule, message string) {
			diagnostics := LoadNamed("review.prompt", source).Validate(ValidationOptions{})
			Expect(diagnostics).To(ContainElement(And(
				HaveField("Rule", rule), HaveField("Severity", "error"),
				HaveField("Message", ContainSubstring(message)),
			)))
		},
		Entry("unclosed frontmatter", "---\nmodel: sonnet\n", "promptfile/frontmatter", "closing"),
		Entry("malformed YAML", "---\nmodel: [unfinished\n---\nbody\n", "promptfile/frontmatter", "yaml"),
		Entry("duplicate YAML key", "---\neffort: low\neffort: high\n---\nbody\n", "promptfile/frontmatter", "already defined"),
		Entry("unknown field", "---\nmaxTruns: 2\n---\nbody\n", "promptfile/frontmatter", "maxTruns"),
		Entry("invalid option", "---\ntemperature: 9\n---\nbody\n", "promptfile/spec", "temperature"),
		Entry("invalid dotprompt option", "---\nconfig:\n  temperature: 9\n---\nbody\n", "promptfile/spec", "temperature"),
		Entry("non-numeric dotprompt option", "---\nconfig:\n  temperature: warm\n---\nbody\n", "promptfile/spec", "number"),
		Entry("fractional output-token limit", "---\nconfig:\n  maxOutputTokens: 1.5\n---\nbody\n", "promptfile/spec", "integer"),
		Entry("non-finite temperature", "---\nconfig:\n  temperature: .nan\n---\nbody\n", "promptfile/spec", "finite"),
		Entry("invalid compact runtime declaration", "---\nruntimes: [\"agent:sonnet:invalid\", \"agent:sonnet\"]\n---\nbody\n", "promptfile/frontmatter", "effort"),
		Entry("invalid runtime option", "---\nruntimes: [{model: sonnet, temperature: 9}, {model: sonnet}]\n---\nbody\n", "promptfile/spec", "temperature"),
		Entry("invalid body template", "{{#if enabled}}\nReview {{name}}\n", "promptfile/template", "Parse error"),
		Entry("invalid frontmatter template", "---\n{{#if enabled}}\nmodel: sonnet\n---\nbody\n", "promptfile/template", "Parse error"),
		Entry("invalid hook", "---\nhook: {on: plan.revision, mode: invalid}\n---\nbody\n", "promptfile/hook", "mode"),
		Entry("invalid schema declaration", "---\noutput: {schemaType: invalid}\n---\nbody\n", "promptfile/schema", "schema type"),
		Entry("invalid JSON Schema", "---\noutput:\n  schema:\n    type: invalid\n---\nbody\n", "promptfile/schema", "invalid"),
		Entry("invalid schema shape", "---\noutput: {schema: 42}\n---\nbody\n", "promptfile/schema", "schema"),
		Entry("unknown schema field", "---\noutput: {scheam: {answer: string}}\n---\nbody\n", "promptfile/schema", "scheam"),
		Entry("invalid input defaults", "---\ninput: {default: 42}\n---\nbody\n", "promptfile/schema", "default"),
		Entry("invalid TypeSafe questions", "---\noutput:\n  schemaType: typesafe-schema\n  schema:\n    review: {type: choice}\n---\nbody\n", "promptfile/schema", "review"),
		Entry("tools as a dotprompt name list", "---\ntools: [lookup]\n---\nbody\n", "promptfile/frontmatter", "tools must be a mapping"),
		Entry("tool without exec", "---\ntools:\n  lookup: {description: d}\n---\nbody\n", "promptfile/spec", "tools.lookup: exec is required"),
		Entry("tool readonly and destructive", "---\ntools:\n  lookup: {exec: 'true', readonly: true, destructive: true}\n---\nbody\n", "promptfile/spec", "mutually exclusive"),
		Entry("tool with an invalid timeout", "---\ntools:\n  lookup: {exec: 'true', timeout: soon}\n---\nbody\n", "promptfile/spec", "invalid timeout"),
		Entry("tool with an unknown field", "---\ntools:\n  lookup: {exec: 'true', retires: 3}\n---\nbody\n", "promptfile/frontmatter", "retires"),
		Entry("tool with an invalid input schema", "---\ntools:\n  lookup:\n    exec: 'true'\n    input: {type: invalid}\n---\nbody\n", "promptfile/tools", "tools.lookup: input"),
		Entry("tool with an invalid output schema", "---\ntools:\n  lookup:\n    exec: 'true'\n    output: {id: notatype}\n---\nbody\n", "promptfile/tools", "tools.lookup: output"),
	)

	DescribeTable("warns where an inline tool's credentials end up somewhere other than declared",
		func(source, message string) {
			Expect(Load(source).Validate(ValidationOptions{})).To(ConsistOf(And(
				HaveField("Rule", "promptfile/tools"), HaveField("Severity", "warning"),
				HaveField("Message", ContainSubstring(message)),
			)))
		},
		Entry("direct credentials inside the sandbox",
			"---\ntools:\n  deploy: {exec: 'true', placement: sandbox, credentials: direct}\n---\nbody\n",
			"tools.deploy hands real credentials into the sandbox"),
		Entry("a proxied tool beside a setup that hands the agent the same secrets",
			"---\nsetup:\n  connections:\n    aws: {accessKey: {value: a}, secretKey: {value: b}}\ntools:\n  deploy: {exec: 'true', credentials: proxied}\n---\nbody\n",
			"setup.connections (aws) still reach the agent"),
	)

	It("warns that retrying a destructive tool re-runs its side effect", func() {
		const source = "---\ntools:\n  drop: {exec: 'true', destructive: true, retry: {attempts: 2}}\n---\nbody\n"
		Expect(Load(source).Validate(ValidationOptions{})).To(ConsistOf(And(
			HaveField("Rule", "promptfile/tools"), HaveField("Severity", "warning"),
			HaveField("Message", ContainSubstring("tools.drop")),
		)))
	})

	DescribeTable("accepts reusable declarations without runtime configuration",
		func(source string) {
			Expect(Load(source).Validate(ValidationOptions{})).To(BeEmpty())
		},
		Entry("body variables", "{{role \"user\"}}\nReview {{missing}}\n"),
		Entry("body-only", "Review this change.\n"),
		Entry("empty frontmatter", "---\n---\nReview {{missing}}\n"),
		Entry("preset-only", "---\npresets: [reviewer]\n---\n"),
		Entry("custom model", "---\nmodel: tenant-review-model\nmode: api\n---\nReview {{missing}}\n"),
		Entry("picoschema", "---\noutput:\n  schema:\n    answer: string\n---\nReview.\n"),
		Entry("JSON Schema", "---\noutput:\n  schema: {type: object, properties: {answer: {type: string}}}\n---\nReview.\n"),
		Entry("disabled hook", "---\nhook: {disabled: true}\n---\n"),
		Entry("inline tool", "---\ntools:\n  lookup:\n    description: Fetch a ticket\n    input: {id: string}\n    output: {title: string}\n    exec: ./ticket.sh\n    readonly: true\n    timeout: 30s\n    retry: {attempts: 3, exitCodes: [75]}\n---\nReview.\n"),
	)

	It("warns with the missing frontmatter variables and still checks the body", func() {
		const source = "---\nmodel: {{model}}\nbudget:\n  maxTokens: {{limit}}\n---\n{{#if broken}}\n"
		diagnostics := Load(source).Validate(ValidationOptions{Data: map[string]any{"model": "sonnet"}})
		Expect(diagnostics).To(ContainElement(And(HaveField("Rule", "promptfile/variables"), HaveField("Severity", "warning"), HaveField("Message", ContainSubstring("limit")))))
		Expect(diagnostics).To(ContainElement(HaveField("Rule", "promptfile/template")))
	})

	It("validates rendered declarations with complete inputs", func() {
		const source = "---\nbudget:\n  maxTokens: {{limit}}\n---\nReview {{bodyOnly}}\n"
		Expect(Load(source).Validate(ValidationOptions{Data: map[string]any{"limit": 100}})).To(BeEmpty())
		diagnostics := Load(source).Validate(ValidationOptions{Data: map[string]any{"limit": "[unfinished"}})
		Expect(diagnostics).To(ContainElement(HaveField("Rule", "promptfile/frontmatter")))
	})

	It("reports nested missing inputs instead of rendering empty values", func() {
		const source = "---\nbudget:\n  maxTokens: {{options.limit}}\n---\nReview.\n"
		diagnostics := Load(source).Validate(ValidationOptions{Data: map[string]any{"options": map[string]any{}}})
		Expect(diagnostics).To(ContainElement(HaveField("Message", ContainSubstring("options.limit"))))
	})

	It("checks templated conditional schemas with supplied values", func() {
		Expect(Load(templatedSchemaPrompt).Validate(ValidationOptions{Data: map[string]any{"limit": 3}})).To(BeEmpty())
		Expect(Load(templatedSchemaPrompt).Validate(ValidationOptions{})).To(ContainElement(HaveField("Severity", "warning")))
	})

	It("supports TypeSafe declarations using the real reusable fixture", func() {
		source, err := os.ReadFile("testdata/fixtures/jev.prompt")
		Expect(err).NotTo(HaveOccurred())
		Expect(Load(string(source)).Validate(ValidationOptions{})).To(BeEmpty())
	})

	It("reports YAML positions in the original source", func() {
		const source = "# review\n---\nmodel: sonnet\nmaxTruns: 2\n---\nReview.\n"
		diagnostics := Load(source).Validate(ValidationOptions{})
		Expect(diagnostics).To(ContainElement(And(HaveField("Rule", "promptfile/frontmatter"), HaveField("Line", 4))))
	})

	DescribeTable("resolves frontmatter variables in conditional and scoped expressions",
		func(frontmatter string, data map[string]any) {
			Expect(Load("---\n" + frontmatter + "---\nbody\n").Validate(ValidationOptions{Data: data})).To(BeEmpty())
		},
		Entry("inactive branch", "{{#if enabled}}\nbudget:\n  maxTokens: {{missing}}\n{{/if}}\n", map[string]any{"enabled": false}),
		Entry("with scope", "{{#with limits}}\nbudget:\n  maxTokens: {{tokens}}\n{{/with}}\n", map[string]any{"limits": map[string]any{"tokens": 100}}),
		Entry("with alias", "{{#with limits as |values|}}\nbudget:\n  maxTokens: {{values.tokens}}\n{{/with}}\n", map[string]any{"limits": map[string]any{"tokens": 100}}),
		Entry("each alias and root", "runtimes:\n{{#each names as |name|}}\n  - model: {{name}}\n    effort: {{@root.effort}}\n{{/each}}\n", map[string]any{"names": []string{"sonnet", "opus"}, "effort": "low"}),
		Entry("lookup condition", "{{#if (lookup flags \"enabled\")}}\nbudget:\n  maxTokens: {{missing}}\n{{/if}}\n", map[string]any{"flags": map[string]any{"enabled": false}}),
	)
})
