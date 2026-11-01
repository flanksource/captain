package adapters

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = ginkgo.Describe("resource schema presentation", func() {
	ginkgo.It("annotates nested properties, definitions, maps, arrays and union branches", func() {
		documents, err := Schemas()
		Expect(err).NotTo(HaveOccurred())
		for _, document := range documents {
			var root map[string]any
			Expect(json.Unmarshal(document.Schema, &root)).To(Succeed())
			walkPresentation(root, document.ID)
		}
	})

	ginkgo.It("accounts for every captured CLI flag and alias", func() {
		for _, document := range loadAdapterSchemas() {
			if document.body["x-captain-inventory"] == nil || stringField(objectField(document.body, "x-captain-adapter", document.path), "mode") != "cli" {
				continue
			}
			mapped := map[string]bool{}
			for _, raw := range objectField(document.body, "properties", document.path) {
				option := objectField(raw.(map[string]any), "x-captain-option", document.path)
				mapped[stringField(option, "nativeName")] = true
				if aliases, ok := option["aliases"].([]any); ok {
					for _, alias := range aliases {
						mapped[alias.(string)] = true
					}
				}
			}
			flags := objectField(document.body, "x-captain-inventory", document.path)["flags"].([]any)
			Expect(len(flags)).To(BeNumerically(">", 10), document.path)
			for _, flag := range flags {
				Expect(mapped).To(HaveKeyWithValue(flag, true), document.path)
			}
		}
	})
})

func walkPresentation(node map[string]any, path string) {
	Expect(node["title"]).To(BeAssignableToTypeOf(""), path)
	links := objectField(node, "x-help", path)["links"].([]any)
	Expect(links).NotTo(BeEmpty(), path)
	for _, raw := range links {
		href, err := url.Parse(stringField(raw.(map[string]any), "href"))
		Expect(err).NotTo(HaveOccurred(), path)
		Expect(href.Scheme).To(BeElementOf("https", "http"), path)
		Expect(href.Host).NotTo(BeEmpty(), path)
	}
	for _, keyword := range []string{"properties", "$defs", "definitions", "patternProperties"} {
		for key, raw := range objectFieldOrEmpty(node, keyword) {
			if child, ok := raw.(map[string]any); ok {
				walkPresentation(child, path+"/"+keyword+"/"+key)
			}
		}
	}
	for _, keyword := range []string{"items", "additionalProperties", "propertyNames", "if", "then", "else", "not"} {
		if child, ok := node[keyword].(map[string]any); ok {
			walkPresentation(child, path+"/"+keyword)
		}
	}
	for _, keyword := range []string{"oneOf", "anyOf", "allOf", "prefixItems"} {
		if children, ok := node[keyword].([]any); ok {
			for index, raw := range children {
				if child, ok := raw.(map[string]any); ok {
					walkPresentation(child, fmt.Sprintf("%s/%s/%d", path, keyword, index))
				}
			}
		}
	}
}
