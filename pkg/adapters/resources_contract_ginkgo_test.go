package adapters

import (
	"bytes"
	"encoding/json"

	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

var _ = ginkgo.Describe("resource schema catalog", func() {
	ginkgo.It("includes native configuration, permission profiles and portable Captain resources", func() {
		documents, err := Schemas()
		Expect(err).NotTo(HaveOccurred())
		raw, err := json.Marshal(documents)
		Expect(err).NotTo(HaveOccurred())
		var catalog []map[string]any
		Expect(json.Unmarshal(raw, &catalog)).To(Succeed())
		ids := make([]any, 0, len(catalog))
		for _, document := range catalog {
			ids = append(ids, document["id"])
		}
		Expect(ids).To(ContainElements(
			"openai/config", "openai/permission-profile", "anthropic/settings", "anthropic/sandbox",
			"google/settings", "captain/sandbox", "captain/permissions", "captain/preset",
		))
		Expect(ids).To(HaveEach(Not(BeNil())))
		Expect(ids).To(ConsistOf(uniqueResourceIDs(ids)))
	})

	ginkgo.It("compiles every catalog document with self-contained references", func() {
		documents, err := Schemas()
		Expect(err).NotTo(HaveOccurred())
		for _, document := range documents {
			body, err := jsonschema.UnmarshalJSON(bytes.NewReader(document.Schema))
			Expect(err).NotTo(HaveOccurred(), document.Title)
			compiler := jsonschema.NewCompiler()
			Expect(compiler.AddResource("mem:///resource.json", body)).To(Succeed())
			_, err = compiler.Compile("mem:///resource.json")
			Expect(err).NotTo(HaveOccurred(), document.Title)
		}
	})

	ginkgo.It("reports the failing type through referenced permission definitions", func() {
		value, err := jsonschema.UnmarshalJSON(bytes.NewBufferString(`{"workspace_roots":{"/workspace":"yes"}}`))
		Expect(err).NotTo(HaveOccurred())
		result, err := ValidateResource("openai/permission-profile", value)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Errors).To(HaveLen(1))
		Expect(result.Errors[0].InstancePath).To(Equal("/workspace_roots/~1workspace"))
		Expect(result.Errors[0].Message).To(ContainSubstring("boolean"))
	})

	ginkgo.It("rejects invalid nested sandbox values and preserves named profile references", func() {
		for _, example := range []struct {
			id    string
			value string
			valid bool
		}{
			{"captain/sandbox", `{"mode":"native","policy":{"network":{"httpProxyPort":70000}}}`, false},
			{"captain/permissions", `{"mode":"plan","tools":{"Bash":"deny"}}`, true},
			{"captain/permissions", `{"tools":{"Bash":"unknown"}}`, false},
			{"openai/permission-profile", `{"extends":"team-workspace","workspace_roots":{"/workspace":true}}`, true},
			{"openai/permission-profile", `{"network":{"enabled":"yes"}}`, false},
			{"openai/permission-profile", `{"workspace_roots":{"/workspace":"yes"}}`, false},
			{"openai/filesystem", `{"/workspace":"write","/private":"deny"}`, true},
			{"openai/filesystem", `{"/workspace":"execute"}`, false},
			{"captain/docker-backend", `{"kind":"docker","env":{"EXAMPLE":42}}`, false},
			{"captain/git-agent-backend", `{"kind":"git-agent","relay":"invalid"}`, false},
			{"anthropic/sandbox", `{"network":{"allowedDomains":["example.com"]}}`, true},
			{"anthropic/sandbox", `{"network":{"allowedDomains":42}}`, false},
			{"anthropic/api", `{"model":"claude-sonnet-5","max_tokens":128,"messages":[{"role":"user","content":[{"type":"text","text":"Inspect the example"}]}]}`, true},
			{"anthropic/api", `{"model":"claude-sonnet-5","max_tokens":128,"messages":[{"role":"user","content":[{"type":"text","text":42}]}]}`, false},
		} {
			value, err := jsonschema.UnmarshalJSON(bytes.NewBufferString(example.value))
			Expect(err).NotTo(HaveOccurred())
			result, err := ValidateResource(example.id, value)
			Expect(err).NotTo(HaveOccurred(), example.id)
			Expect(result.Valid).To(Equal(example.valid), "%s: %v", example.id, result.Errors)
		}
	})
})

func uniqueResourceIDs(ids []any) []any {
	seen := map[any]bool{}
	var unique []any
	for _, id := range ids {
		if !seen[id] {
			unique = append(unique, id)
			seen[id] = true
		}
	}
	return unique
}
