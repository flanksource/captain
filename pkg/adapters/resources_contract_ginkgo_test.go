package adapters

import (
	"bytes"
	"encoding/json"

	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

var _ = ginkgo.Describe("resource schema catalog", func() {
	ginkgo.It("embeds Captain-owned execution and portable resources, leaving native provider resources to the published catalog", func() {
		documents, err := Schemas()
		Expect(err).NotTo(HaveOccurred())
		ids := make([]any, 0, len(documents))
		for _, document := range documents {
			ids = append(ids, document.ID)
		}
		Expect(ids).To(ContainElements("anthropic/api", "openai/cli", "captain/sandbox", "captain/permissions",
			"captain/preset", "captain/docker-backend", "captain/git-agent-backend"))
		for _, projection := range resourceProjections {
			Expect(ids).NotTo(ContainElement(projection.provider + "/" + projection.name))
		}
		Expect(ids).To(ConsistOf(uniqueResourceIDs(ids)))
	})

	ginkgo.It("refuses to project native resources when a provider source is missing", func() {
		_, err := NativeResourceSchemas(map[string]map[string]any{"openai": {"properties": map[string]any{}}})
		Expect(err).To(MatchError(ContainSubstring("anthropic")))
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

	ginkgo.It("rejects invalid nested sandbox values and preserves named profile references", func() {
		for _, example := range []struct {
			id    string
			value string
			valid bool
		}{
			{"captain/sandbox", `{"mode":"native","policy":{"network":{"httpProxyPort":70000}}}`, false},
			{"captain/permissions", `{"mode":"plan","tools":{"Bash":"deny"}}`, true},
			{"captain/permissions", `{"tools":{"Bash":"unknown"}}`, false},
			{"captain/docker-backend", `{"kind":"docker","env":{"EXAMPLE":42}}`, false},
			{"captain/git-agent-backend", `{"kind":"git-agent","relay":"invalid"}`, false},
			{"anthropic/api", `{"model":"claude-sonnet-5","max_tokens":128,"messages":[{"role":"user","content":[{"type":"text","text":"Inspect the example"}]}]}`, true},
			{"anthropic/api", `{"model":"claude-sonnet-5","max_tokens":128,"messages":[{"role":"user","content":[{"type":"text","text":42}]}]}`, false},
		} {
			result, err := ValidateResource(ValidationRequest{ID: example.id, Value: decodeInstance(example.value)})
			Expect(err).NotTo(HaveOccurred(), example.id)
			Expect(result.Valid).To(Equal(example.valid), "%s: %v", example.id, result.Errors)
		}
	})

	ginkgo.It("validates a published document against the schema it carries and reports the failing instance path", func() {
		schema := json.RawMessage(`{"type":"object","properties":{"roots":{"type":"object","additionalProperties":{"$ref":"#/$defs/Flag"}}},"$defs":{"Flag":{"type":"boolean"}}}`)
		result, err := ValidateResource(ValidationRequest{ID: "openai/permission-profile", Schema: schema,
			Value: decodeInstance(`{"roots":{"/workspace":"yes"}}`)})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Valid).To(BeFalse())
		Expect(result.Errors).To(HaveLen(1))
		Expect(result.Errors[0].InstancePath).To(Equal("/roots/~1workspace"))
		Expect(result.Errors[0].Message).To(ContainSubstring("boolean"))
	})

	ginkgo.It("never loads external references from a carried schema", func() {
		_, err := ValidateResource(ValidationRequest{ID: "example/remote", Schema: json.RawMessage(`{"$ref":"file:///etc/hosts"}`),
			Value: decodeInstance(`{}`)})
		Expect(err).To(MatchError(ContainSubstring("example/remote")))
	})

	ginkgo.It("reports an unknown embedded schema id", func() {
		_, err := ValidateResource(ValidationRequest{ID: "anthropic/settings", Value: decodeInstance(`{}`)})
		Expect(err).To(MatchError(ErrUnknownSchema))
	})
})

func decodeInstance(raw string) any {
	value, err := jsonschema.UnmarshalJSON(bytes.NewBufferString(raw))
	Expect(err).NotTo(HaveOccurred())
	return value
}

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
