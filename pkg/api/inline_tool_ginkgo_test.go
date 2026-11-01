package api_test

import (
	"encoding/json"
	"time"

	"github.com/flanksource/commons-db/shell"
	"github.com/flanksource/commons-db/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"

	"github.com/flanksource/captain/pkg/api"
)

const inlineToolYAML = `
tools:
  lookup_ticket:
    description: Fetch a ticket
    input: {id: string}
    output: {title: string}
    exec: ./ticket.sh
    readonly: true
    timeout: 30s
    retry: {attempts: 3, delay: 2s, exitCodes: [75]}
    dotenv: [.env]
    envVars:
      - name: TOKEN
        value: abc
`

var _ = Describe("InlineTools", func() {
	It("decodes the frontmatter shape, inline setup fields included", func() {
		var spec api.Spec
		Expect(yaml.Unmarshal([]byte(inlineToolYAML), &spec)).To(Succeed())

		Expect(spec.Tools).To(Equal(api.InlineTools{"lookup_ticket": {
			Setup: shell.Setup{
				DotEnv:  []string{".env"},
				EnvVars: []types.EnvVar{{Name: "TOKEN", ValueStatic: "abc"}},
			},
			Description: "Fetch a ticket",
			Input:       map[string]any{"id": "string"},
			Output:      map[string]any{"title": "string"},
			Exec:        "./ticket.sh",
			ReadOnly:    true,
			Timeout:     "30s",
			Retry:       &api.ToolRetry{Attempts: 3, Delay: "2s", ExitCodes: []int{75}},
		}}))
		Expect(spec.Validate()).To(MatchError(ContainSubstring("model")), "only the model is missing")
	})

	It("round-trips through JSON", func() {
		var spec api.Spec
		Expect(yaml.Unmarshal([]byte(inlineToolYAML), &spec)).To(Succeed())
		encoded, err := json.Marshal(spec)
		Expect(err).NotTo(HaveOccurred())

		var decoded api.Spec
		Expect(json.Unmarshal(encoded, &decoded)).To(Succeed())
		Expect(decoded.Tools).To(Equal(spec.Tools))
	})

	// A tool is indivisible: one layer's exec under another layer's schema would
	// describe a tool neither author wrote.
	It("merges layers by tool name, replacing a redeclared tool whole", func() {
		base := api.Spec{Tools: api.InlineTools{
			"keep":     {Exec: "echo keep"},
			"redefine": {Exec: "echo old", Description: "old description"},
		}}
		override := api.Spec{Tools: api.InlineTools{
			"redefine": {Exec: "echo new"},
			"added":    {Exec: "echo added"},
		}}

		merged := base.Merge(override).Tools

		Expect(merged).To(Equal(api.InlineTools{
			"keep":     {Exec: "echo keep"},
			"redefine": {Exec: "echo new"},
			"added":    {Exec: "echo added"},
		}))
	})

	DescribeTable("rejects invalid declarations",
		func(tools api.InlineTools, message string) {
			Expect(tools.Validate()).To(MatchError(ContainSubstring(message)))
		},
		Entry("a name providers cannot carry", api.InlineTools{"bad name": {Exec: "true"}}, "letters, digits"),
		Entry("a missing exec", api.InlineTools{"t": {}}, "exec is required"),
		Entry("readonly and destructive together",
			api.InlineTools{"t": {Exec: "true", ReadOnly: true, Destructive: true}}, "mutually exclusive"),
		Entry("an unknown permission", api.InlineTools{"t": {Exec: "true", Permission: "maybe"}}, "invalid permission"),
		Entry("a per-tool checkout",
			api.InlineTools{"t": {Exec: "true", Setup: shell.Setup{Checkout: &shell.Checkout{URL: "https://example.com/r.git"}}}},
			"checkout is not supported"),
		Entry("an unparsable timeout", api.InlineTools{"t": {Exec: "true", Timeout: "soon"}}, "invalid timeout"),
		Entry("zero retry attempts", api.InlineTools{"t": {Exec: "true", Retry: &api.ToolRetry{}}}, "retry.attempts"),
		Entry("a shrinking backoff",
			api.InlineTools{"t": {Exec: "true", Retry: &api.ToolRetry{Attempts: 2, Backoff: 0.5}}}, "retry.backoff"),
		Entry("maxDelay below delay",
			api.InlineTools{"t": {Exec: "true", Retry: &api.ToolRetry{Attempts: 2, Delay: "5s", MaxDelay: "1s"}}}, "retry.maxDelay"),
	)

	It("applies retry defaults and treats no retry as one attempt", func() {
		Expect((*api.ToolRetry)(nil).Plan()).To(Equal(api.RetryPlan{Attempts: 1}))
		Expect((&api.ToolRetry{Attempts: 4}).Plan()).To(Equal(api.RetryPlan{
			Attempts: 4, Delay: time.Second, MaxDelay: 30 * time.Second, Backoff: 2,
		}))
	})

	DescribeTable("projects readonly/destructive onto the MCP hint pair",
		func(tool api.InlineTool, readOnly, destructive *bool) {
			gotReadOnly, gotDestructive := tool.Hints()
			Expect(gotReadOnly).To(Equal(readOnly))
			Expect(gotDestructive).To(Equal(destructive))
		},
		Entry("undeclared claims nothing", api.InlineTool{}, nil, nil),
		Entry("readonly is non-destructive", api.InlineTool{ReadOnly: true}, ptr(true), ptr(false)),
		Entry("destructive is not readonly", api.InlineTool{Destructive: true}, ptr(false), ptr(true)),
	)
})

func ptr[T any](value T) *T { return &value }
