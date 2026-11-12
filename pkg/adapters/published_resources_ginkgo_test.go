//go:build adapterresources

package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// These specs check the generated catalog that CI publishes to the schemas branch.
// Run them after `go run ./hack/adapterresources resources` with
// CAPTAIN_ADAPTER_RESOURCES pointing at its -out directory.
var _ = ginkgo.Describe("published native resource catalog", func() {
	var documents map[string]SchemaDocument

	ginkgo.BeforeEach(func() {
		dir := os.Getenv("CAPTAIN_ADAPTER_RESOURCES")
		Expect(dir).NotTo(BeEmpty(), "CAPTAIN_ADAPTER_RESOURCES must name the generated adapter resource directory")
		raw, err := os.ReadFile(filepath.Join(dir, NativeResourceCatalogFile))
		Expect(err).NotTo(HaveOccurred())
		var catalog []SchemaDocument
		Expect(json.Unmarshal(raw, &catalog)).To(Succeed())
		documents = map[string]SchemaDocument{}
		for _, document := range catalog {
			Expect(documents).NotTo(HaveKey(document.ID))
			documents[document.ID] = document
		}
	})

	ginkgo.It("publishes every native projection with presentation annotations", func() {
		Expect(documents).To(HaveLen(len(resourceProjections)))
		for _, projection := range resourceProjections {
			id := projection.provider + "/" + projection.name
			Expect(documents).To(HaveKey(id))
			var root map[string]any
			Expect(json.Unmarshal(documents[id].Schema, &root)).To(Succeed())
			walkPresentation(root, id)
		}
	})

	ginkgo.It("validates native examples against the published schemas", func() {
		for _, example := range []struct {
			id    string
			value string
			valid bool
		}{
			{"openai/permission-profile", `{"extends":"team-workspace","workspace_roots":{"/workspace":true}}`, true},
			{"openai/permission-profile", `{"network":{"enabled":"yes"}}`, false},
			{"openai/permission-profile", `{"workspace_roots":{"/workspace":"yes"}}`, false},
			{"openai/filesystem", `{"/workspace":"write","/private":"deny"}`, true},
			{"openai/filesystem", `{"/workspace":"execute"}`, false},
			{"anthropic/sandbox", `{"network":{"allowedDomains":["example.com"]}}`, true},
			{"anthropic/sandbox", `{"network":{"allowedDomains":42}}`, false},
			{"anthropic/permissions", `{"defaultMode":"manual"}`, true},
			{"anthropic/settings", `{"cleanupPeriodDays":1.5}`, false},
			{"google/settings", `{"model":{"name":"gemini-example"}}`, true},
		} {
			result, err := ValidateResource(ValidationRequest{ID: example.id, Schema: documents[example.id].Schema,
				Value: decodeInstance(example.value)})
			Expect(err).NotTo(HaveOccurred(), example.id)
			Expect(result.Valid).To(Equal(example.valid), "%s %s: %v", example.id, example.value, result.Errors)
		}
	})

	ginkgo.It("applies the declarative help-text patches", func() {
		var sandbox map[string]any
		Expect(json.Unmarshal(documents["anthropic/sandbox"].Schema, &sandbox)).To(Succeed())
		Expect(sandbox["description"]).To(ContainSubstring("sandbox"))
		enabled := objectField(objectField(sandbox, "properties", "anthropic/sandbox"), "enabled", "anthropic/sandbox")
		Expect(enabled["description"]).To(ContainSubstring("Bash"))
	})
})
