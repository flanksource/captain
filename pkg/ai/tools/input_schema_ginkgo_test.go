package tools_test

import (
	"context"
	"encoding/json"

	"github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type schemaQuestion struct {
	Text string `json:"text" jsonschema:"required"`
}

type schemaEnvelope struct {
	Summary   string           `json:"summary" jsonschema:"required"`
	Questions []schemaQuestion `json:"questions,omitempty"`
}

type schemaNode struct {
	Name     string       `json:"name"`
	Children []schemaNode `json:"children,omitempty"`
}

func reflectedSchema(value any) map[string]any {
	raw, err := api.SchemaJSON(value)
	Expect(err).NotTo(HaveOccurred())
	var schema map[string]any
	Expect(json.Unmarshal(raw, &schema)).To(Succeed())
	return schema
}

var _ = Describe("Caller tool input schemas", func() {
	noop := func(context.Context, map[string]any) (any, error) { return "ok", nil }

	resolveSchema := func(schema map[string]any) (map[string]any, error) {
		definitions, err := tools.ResolveDefinitions([]api.ToolDefinition{
			{Name: "triage_prepare", InputSchema: schema, Handler: noop},
		}, tools.ResolveOptions{})
		if err != nil {
			return nil, err
		}
		return definitions[0].InputSchema, nil
	}

	It("resolves a reflected $ref/$defs schema into a self-contained root object", func() {
		schema := reflectedSchema(&schemaEnvelope{})
		Expect(schema).To(HaveKey("$ref"))

		resolved, err := resolveSchema(schema)

		Expect(err).NotTo(HaveOccurred())
		Expect(resolved).To(Equal(map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"summary"},
			"properties": map[string]any{
				"summary": map[string]any{"type": "string"},
				"questions": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"required":             []any{"text"},
						"properties":           map[string]any{"text": map[string]any{"type": "string"}},
					},
				},
			},
		}))
	})

	It("adds empty properties to a parameterless object schema", func() {
		resolved, err := resolveSchema(map[string]any{"type": "object"})

		Expect(err).NotTo(HaveOccurred())
		Expect(resolved).To(Equal(map[string]any{"type": "object", "properties": map[string]any{}}))
	})

	It("gives a tool without a schema an empty object schema", func() {
		resolved, err := resolveSchema(nil)

		Expect(err).NotTo(HaveOccurred())
		Expect(resolved).To(Equal(map[string]any{"type": "object", "properties": map[string]any{}}))
	})

	It("leaves the caller's schema map unchanged", func() {
		schema := reflectedSchema(&schemaEnvelope{})
		original := reflectedSchema(&schemaEnvelope{})

		_, err := resolveSchema(schema)

		Expect(err).NotTo(HaveOccurred())
		Expect(schema).To(Equal(original))
	})

	It("rejects a recursive $ref it cannot inline", func() {
		_, err := resolveSchema(reflectedSchema(&schemaNode{}))

		Expect(err).To(MatchError(ContainSubstring(`caller tool "triage_prepare" input schema`)))
		Expect(err).To(MatchError(ContainSubstring("recursive $ref")))
	})

	It("rejects a root that is not an object", func() {
		_, err := resolveSchema(map[string]any{"type": "string"})

		Expect(err).To(MatchError(ContainSubstring(`caller tool "triage_prepare" input schema must be a JSON object schema, got type "string"`)))
	})

	It("keeps a property named $ref and $ref-shaped literal values", func() {
		refLiteral := "#/$defs/Missing"
		resolved, err := resolveSchema(map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"$ref":  map[string]any{"type": "string", "default": refLiteral},
				"thing": map[string]any{"const": map[string]any{"$ref": refLiteral}},
			},
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(resolved).To(Equal(map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"$ref":  map[string]any{"type": "string", "default": refLiteral},
				"thing": map[string]any{"const": map[string]any{"$ref": refLiteral}},
			},
		}))
	})

	It("skips schema resolution for a denied tool so allowed tools still resolve", func() {
		definitions, err := tools.ResolveDefinitions([]api.ToolDefinition{
			{Name: "tree_walk", InputSchema: reflectedSchema(&schemaNode{}), DefaultPermission: api.ToolPolicyDeny, Handler: noop},
			{Name: "triage_prepare", InputSchema: map[string]any{"type": "object"}, Handler: noop},
		}, tools.ResolveOptions{})

		Expect(err).NotTo(HaveOccurred())
		Expect(definitions).To(HaveLen(1))
		Expect(definitions[0].Name).To(Equal("triage_prepare"))
	})

	It("rejects a $ref outside #/$defs", func() {
		_, err := resolveSchema(map[string]any{"$ref": "https://example.com/schema.json"})

		Expect(err).To(MatchError(ContainSubstring(`unsupported $ref "https://example.com/schema.json"`)))
	})
})
