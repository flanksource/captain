package api

import (
	"encoding/json"
	"reflect"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

var _ = Describe("SandboxRef", func() {
	DescribeTable("round-trips the scalar form",
		func(marshal func(any) ([]byte, error), decode func([]byte, any) error) {
			var ref SandboxRef
			Expect(decode(mustMarshal(marshal, SandboxRef{Mode: SandboxNative}), &ref)).To(Succeed())
			Expect(ref).To(Equal(SandboxRef{Mode: SandboxNative}))
		},
		Entry("JSON", json.Marshal, json.Unmarshal),
		Entry("YAML", yaml.Marshal, yaml.Unmarshal),
	)

	DescribeTable("round-trips the object form",
		func(marshal func(any) ([]byte, error), decode func([]byte, any) error) {
			full := SandboxRef{
				Mode:     SandboxGitAgent,
				Backend:  "prod-pool",
				Agent:    "worker-01",
				Dispatch: &SandboxDispatchPolicy{Paths: []string{"pkg/**", "!**/*.pem"}, MaxAttempts: 3},
			}
			var ref SandboxRef
			Expect(decode(mustMarshal(marshal, full), &ref)).To(Succeed())
			Expect(ref).To(Equal(full))
		},
		Entry("JSON", json.Marshal, json.Unmarshal),
		Entry("YAML", yaml.Marshal, yaml.Unmarshal),
	)

	It("decodes a bare YAML mode", func() {
		var ref SandboxRef
		Expect(yaml.Unmarshal([]byte(`git-agent`), &ref)).To(Succeed())
		Expect(ref).To(Equal(SandboxRef{Mode: SandboxGitAgent}))
	})

	It("emits the scalar form when only the mode is set", func() {
		encoded, err := yaml.Marshal(SandboxRef{Mode: SandboxDocker})
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(string(encoded))).To(Equal("docker"))
	})

	DescribeTable("rejects unknown object keys",
		func(decode func([]byte, any) error, document string) {
			var ref SandboxRef
			Expect(decode([]byte(document), &ref)).To(MatchError(ContainSubstring("field")))
		},
		Entry("JSON", json.Unmarshal, `{"mod":"native"}`),
		Entry("YAML", yaml.Unmarshal, "mod: native"),
	)

	It("rejects unknown nested policy keys", func() {
		var ref SandboxRef
		err := yaml.Unmarshal([]byte("mode: native\npolicy: {network: {allowedDomain: [example.com]}}"), &ref)
		Expect(err).To(MatchError(ContainSubstring("allowedDomain")))
	})

	It("leaves the receiver unchanged on explicit JSON null", func() {
		ref := SandboxRef{Mode: SandboxNative}
		Expect(json.Unmarshal([]byte(`null`), &ref)).To(Succeed())
		Expect(ref).To(Equal(SandboxRef{Mode: SandboxNative}))
	})

	It("declares the public modes and permits a named backend awaiting context resolution", func() {
		schema := SandboxRef{}.JSONSchema()
		Expect(schema.OneOf).To(HaveLen(2))
		Expect(schema.OneOf[0].Enum).To(Equal(enumValues(AllSandboxModes())))
		Expect(schema.OneOf[1].Required).To(BeEmpty())
		Expect(schema.OneOf[1].AnyOf).To(HaveLen(2))
		Expect(schema.OneOf[1].AnyOf[0].Required).To(Equal([]string{"mode"}))
		Expect(schema.OneOf[1].AnyOf[1].Required).To(Equal([]string{"backend"}))
	})

	It("rejects a negative dispatch attempt bound", func() {
		err := SandboxRef{
			Mode: SandboxGitAgent, Dispatch: &SandboxDispatchPolicy{MaxAttempts: -1},
		}.Validate()
		Expect(err).To(MatchError(ContainSubstring("maxAttempts")))
	})
})

var _ = Describe("Spec.Sandbox", func() {
	DescribeTable("survives a Spec marshal round-trip",
		func(marshal func(any) ([]byte, error), decode func([]byte, any) error) {
			spec := Spec{Sandbox: &SandboxRef{Mode: SandboxGitAgent, Backend: "prod-pool", Agent: "worker-01"}}
			var decoded Spec
			Expect(decode(mustMarshal(marshal, spec), &decoded)).To(Succeed())
			Expect(decoded.Sandbox).To(Equal(spec.Sandbox))
		},
		Entry("JSON", json.Marshal, json.Unmarshal),
		Entry("YAML", yaml.Marshal, yaml.Unmarshal),
	)

	It("keeps the base sandbox when the override has none", func() {
		base := Spec{Sandbox: &SandboxRef{Mode: SandboxDocker, Backend: "prod-pool"}}
		Expect(base.Merge(Spec{}).Sandbox).To(Equal(base.Sandbox))
	})
})

var _ = Describe("Spec/specMarshal mirror", func() {
	It("declares the same serialized fields in both structs", func() {
		Expect(jsonTagSet(reflect.TypeOf(Spec{}))).To(ConsistOf(jsonTagSet(reflect.TypeOf(specMarshal{}))))
	})
})

func jsonTagSet(t reflect.Type) []string {
	var tags []string
	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() && !field.Anonymous {
			continue
		}
		tag, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if tag == "-" {
			continue
		}
		if tag == "" && field.Anonymous {
			tags = append(tags, jsonTagSet(field.Type)...)
			continue
		}
		tags = append(tags, tag)
	}
	return tags
}

func mustMarshal(marshal func(any) ([]byte, error), value any) []byte {
	encoded, err := marshal(value)
	Expect(err).NotTo(HaveOccurred())
	return encoded
}
