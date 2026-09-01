package api_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("Runtime argument mappings", func() {
	It("publishes a JSON schema for every runtime", func() {
		for _, family := range api.RuntimeCatalog() {
			for _, mode := range family.Modes {
				Expect(mode.Schema).NotTo(BeNil(), "backend %s", mode.Mode)
				Expect(mode.Schema["type"]).To(Equal("object"), "backend %s", mode.Mode)
				Expect(mode.Schema["properties"]).To(BeAssignableToTypeOf(map[string]any{}), "backend %s", mode.Mode)
			}
		}
	})

	It("annotates provider-specific fields with their native bindings", func() {
		codexMode := runtimeSchemaProperty(api.RuntimeSchemaFor(api.OpenAI, api.ModeCLI), "sandbox", "mode")
		Expect(codexMode["x-icon"]).To(Equal("shield"))
		Expect(codexMode["enum"]).To(Equal(apiEnumValues([]api.SandboxKind{
			api.SandboxOff, api.SandboxNative, api.SandboxDocker, api.SandboxGitAgent,
		})))
		Expect(codexMode["x-clicky-arguments"]).To(ContainElement(map[string]any{
			"name": "--sandbox", "implementation": "mapped",
		}))

		codexApproval := runtimeSchemaProperty(api.RuntimeSchemaFor(api.OpenAI, api.ModeCLI), "permissions", "mode")
		Expect(codexApproval["x-enum-display"]).To(Equal("segmented"))
		Expect(codexApproval["x-clicky-arguments"]).To(ContainElement(map[string]any{
			"name": "approval_policy", "implementation": "mapped",
		}))

		geminiApproval := runtimeSchemaProperty(api.RuntimeSchemaFor(api.Google, api.ModeCLI), "permissions", "mode")
		Expect(geminiApproval["x-clicky-arguments"]).To(ContainElement(map[string]any{
			"name": "--approval-mode", "implementation": "mapped",
		}))
		Expect(runtimeSchemaProperty(api.RuntimeSchemaFor(api.Google, api.ModeCLI), "sandbox", "mode")["enum"]).NotTo(
			ContainElement(string(api.SandboxNative)),
		)

		claudeTurns := runtimeSchemaProperty(api.RuntimeSchemaFor(api.Anthropic, api.ModeAgent), "budget", "maxTurns")
		Expect(claudeTurns["x-icon"]).To(Equal("repeat"))
		Expect(claudeTurns["x-clicky-arguments"]).To(ContainElement(map[string]any{
			"name": "maxTurns", "implementation": "mapped",
		}))
	})

	It("classifies memory and skill controls as model configuration", func() {
		claudeSchema := api.RuntimeSchemaFor(api.Anthropic, api.ModeCLI)
		for _, path := range [][]string{
			{"model"}, {"effort"}, {"fallbacks"}, {"budget", "maxTokens"},
			{"sessionId"}, {"memory", "skipSkills"},
			{"permissions", "skills"},
		} {
			Expect(runtimeSchemaProperty(claudeSchema, path...)["x-clicky-section"]).To(
				Equal("model"), "path %v", path,
			)
		}
		Expect(runtimeSchemaProperty(claudeSchema, "memory", "skipSkills")["type"]).To(Equal("boolean"))
		Expect(runtimeSchemaProperty(claudeSchema, "permissions", "skills")["x-clicky-arguments"]).To(
			ContainElement(map[string]any{"name": "--plugin-dir", "implementation": "mapped"}),
		)
		Expect(runtimeSchemaProperty(claudeSchema, "permissions", "tools")["x-clicky-section"]).To(Equal("permissions"))
		Expect(runtimeSchemaProperty(claudeSchema, "permissions", "mcp", "disabled")["x-clicky-section"]).To(Equal("permissions"))
		Expect(runtimeSchemaProperty(claudeSchema, "permissions", "mode")["x-clicky-section"]).To(Equal("permissions"))

		codexSchema := api.RuntimeSchemaFor(api.OpenAI, api.ModeCLI)
		Expect(runtimeSchemaProperty(codexSchema, "memory", "skipMemory")["x-clicky-section"]).To(Equal("model"))
	})

	It("classifies every published runtime field into a known editor section", func() {
		knownSections := []any{"model", "prompt", "workspace", "sandbox", "permissions", "environment", "cli"}
		for _, runtime := range api.AllRuntimes() {
			p, ok := runtime.ModelProvider()
			Expect(ok).To(BeTrue(), runtime.String())
			assertRuntimeSchemaSections(api.RuntimeSchemaFor(p, runtime.Mode), runtime.String(), knownSections)
		}
	})

	It("keeps transport-managed arguments on the schema root", func() {
		managed := api.RuntimeSchemaFor(api.OpenAI, api.ModeAgent)["x-clicky-managed-arguments"]
		Expect(managed).To(ContainElement(map[string]any{
			"name": "turn/start.input", "implementation": "managed",
			"description": "Captain composes text and prepared local-image inputs.",
		}))
	})

	It("describes structured collections and policy maps with their real JSON shapes", func() {
		schema := api.RuntimeSchemaFor(api.Anthropic, api.ModeAgent)
		Expect(runtimeSchemaProperty(schema, "fallbacks")["items"]).To(Equal(map[string]any{
			"oneOf": []any{
				map[string]any{"type": "string"},
				map[string]any{"type": "object", "additionalProperties": true},
			},
		}))
		Expect(runtimeSchemaProperty(schema, "setup", "envVars")["items"]).To(Equal(
			map[string]any{"type": "object", "additionalProperties": true},
		))
		Expect(runtimeSchemaProperty(schema, "toolPolicy")).To(Equal(map[string]any{
			"type": "array", "title": "ToolPolicy", "x-icon": "shield",
			"x-clicky-section": "permissions",
			"items":            map[string]any{"type": "object", "additionalProperties": true},
		}))
		Expect(runtimeSchemaProperty(schema, "toolPreferences")["additionalProperties"]).To(Equal(
			map[string]any{"type": "string", "enum": apiEnumValues(api.AllToolPolicies())},
		))
	})

	It("publishes common API fields with only the off sandbox mode", func() {
		schema := api.RuntimeSchemaFor(api.OpenAI, api.ModeAPI)
		Expect(func() { runtimeSchemaProperty(schema, "model") }).NotTo(Panic())
		Expect(func() { runtimeSchemaProperty(schema, "prompt", "user") }).NotTo(Panic())
		Expect(runtimeSchemaProperty(schema, "sandbox", "mode")["enum"]).To(Equal([]any{"off"}))
		Expect(func() { runtimeSchemaProperty(schema, "permissions", "mode") }).To(Panic())
	})

	It("publishes only native fields each provider can translate", func() {
		claude := api.RuntimeSchemaFor(api.Anthropic, api.ModeCLI)
		Expect(func() {
			runtimeSchemaProperty(claude, "sandbox", "policy", "network", "allowedDomains")
		}).NotTo(Panic())
		Expect(runtimeSchemaProperty(claude, "sandbox", "policy", "network", "allowedDomains")["title"]).To(
			Equal("Allowed domains"),
		)
		Expect(runtimeSchemaProperty(claude, "sandbox", "policy", "network", "allowedDomains")["x-array-display"]).To(
			Equal("list"),
		)
		Expect(func() {
			runtimeSchemaProperty(claude, "sandbox", "policy", "filesystem", "includeSystemTemp")
		}).To(Panic())

		codex := api.RuntimeSchemaFor(api.OpenAI, api.ModeCLI)
		Expect(runtimeSchemaProperty(codex, "sandbox", "policy", "filesystem")["title"]).To(
			Equal("Filesystem"),
		)
		Expect(runtimeSchemaProperty(codex, "sandbox", "policy", "network")["title"]).To(
			Equal("Network"),
		)
		Expect(func() {
			runtimeSchemaProperty(codex, "sandbox", "policy", "filesystem", "includeSystemTemp")
		}).NotTo(Panic())
		Expect(runtimeSchemaProperty(codex, "sandbox", "policy", "filesystem", "writableRoots")["title"]).To(
			Equal("Writable roots"),
		)
		Expect(runtimeSchemaProperty(codex, "sandbox", "policy", "filesystem", "writableRoots")["x-array-display"]).To(
			Equal("list"),
		)
		Expect(runtimeSchemaProperty(codex, "sandbox", "policy", "filesystem", "includeSystemTemp")["title"]).To(
			Equal("Include system temp"),
		)
		Expect(func() {
			runtimeSchemaProperty(codex, "sandbox", "policy", "network", "allowedDomains")
		}).To(Panic())
	})
})

func apiEnumValues[T ~string](values []T) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = string(value)
	}
	return out
}

func runtimeSchemaProperty(schema map[string]any, path ...string) map[string]any {
	current := schema
	for _, name := range path {
		properties, ok := current["properties"].(map[string]any)
		if !ok {
			panic("runtime schema has no properties")
		}
		next, ok := properties[name].(map[string]any)
		if !ok {
			panic("runtime schema property is missing")
		}
		current = next
	}
	return current
}

func assertRuntimeSchemaSections(schema map[string]any, path string, knownSections []any) {
	properties, _ := schema["properties"].(map[string]any)
	for name, raw := range properties {
		field := raw.(map[string]any)
		fieldPath := path + "." + name
		if nested, ok := field["properties"].(map[string]any); ok && len(nested) > 0 {
			assertRuntimeSchemaSections(field, fieldPath, knownSections)
			continue
		}
		Expect(field["x-clicky-section"]).To(BeElementOf(knownSections...), "field %s", fieldPath)
	}
}
