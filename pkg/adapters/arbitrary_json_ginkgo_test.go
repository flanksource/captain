package adapters

import (
	"github.com/flanksource/captain/pkg/adapters/presentation"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = ginkgo.Describe("arbitrary native JSON presentation", func() {
	ginkgo.It("removes string-list hints when native array items require enum or structured controls", func() {
		for _, items := range []map[string]any{
			{"$ref": "#/$defs/Include"}, {"type": "string", "enum": []any{"read", "write"}},
		} {
			schema := map[string]any{"type": "array", "items": items, "x-array-display": "list"}
			presentation.AnnotateSchema(schema, "https://example.com/schema")
			Expect(schema).NotTo(HaveKeyWithValue("x-array-display", "list"))
		}
	})

	ginkgo.It("uses typed JSON editors for untyped values without constraining their native type", func() {
		schema := map[string]any{"type": "object", "properties": map[string]any{
			"payload": map[string]any{},
			"options": map[string]any{"type": "object", "additionalProperties": true},
			"label":   map[string]any{"type": "string"},
		}}
		presentation.AnnotateSchema(schema, "https://example.com/schema")
		properties := schema["properties"].(map[string]any)
		payload := properties["payload"].(map[string]any)
		Expect(payload).To(HaveKeyWithValue("format", "json"))
		Expect(payload).NotTo(HaveKey("type"))
		options := properties["options"].(map[string]any)["additionalProperties"]
		Expect(options).To(HaveKeyWithValue("format", "json"))
		Expect(properties["label"]).To(HaveKeyWithValue("type", "string"))
	})
})
