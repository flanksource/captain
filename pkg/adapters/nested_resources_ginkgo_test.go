package adapters

import (
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = ginkgo.Describe("native nested resources", func() {
	ginkgo.It("describes native API messages and tools with referenced SDK definitions", func() {
		for _, document := range loadAdapterSchemas() {
			if document.path != "anthropic/api.schema.json" && document.path != "openai/api.schema.json" && document.path != "google/api.schema.json" {
				continue
			}
			definitions := objectFieldOrEmpty(document.body, "$defs")
			Expect(len(definitions)).To(BeNumerically(">", 10), document.path)
			properties := objectField(document.body, "properties", document.path)
			tools := objectField(properties, "tools", document.path)
			Expect(tools).To(HaveKey("items"), document.path)
			Expect(objectField(tools, "items", document.path)).To(HaveKey("$ref"), document.path)
		}
	})
})
