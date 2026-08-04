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
			Expect(decode(mustMarshal(marshal, SandboxRef{Backend: "git-agent"}), &ref)).To(Succeed())
			Expect(ref).To(Equal(SandboxRef{Backend: "git-agent"}))
		},
		Entry("JSON", json.Marshal, json.Unmarshal),
		Entry("YAML", yaml.Marshal, yaml.Unmarshal),
	)

	DescribeTable("round-trips the object form",
		func(marshal func(any) ([]byte, error), decode func([]byte, any) error) {
			full := SandboxRef{
				Backend: "prod-pool",
				Agent:   "worker-01",
				Policy:  &SandboxPolicy{Paths: []string{"pkg/**", "!**/*.pem"}, MaxAttempts: 3},
			}
			var ref SandboxRef
			Expect(decode(mustMarshal(marshal, full), &ref)).To(Succeed())
			Expect(ref).To(Equal(full))
		},
		Entry("JSON", json.Marshal, json.Unmarshal),
		Entry("YAML", yaml.Marshal, yaml.Unmarshal),
	)

	It("decodes a bare YAML scalar", func() {
		var ref SandboxRef
		Expect(yaml.Unmarshal([]byte(`git-agent`), &ref)).To(Succeed())
		Expect(ref).To(Equal(SandboxRef{Backend: "git-agent"}))
	})

	It("emits the scalar form when only the backend is set", func() {
		encoded, err := yaml.Marshal(SandboxRef{Backend: "prod-pool"})
		Expect(err).NotTo(HaveOccurred())
		Expect(strings.TrimSpace(string(encoded))).To(Equal("prod-pool"))
	})

	It("rejects overrides with no backend", func() {
		err := SandboxRef{Agent: "worker-01"}.Validate()
		Expect(err).To(MatchError(ContainSubstring("require a backend")))
	})

	It("rejects a negative attempt bound", func() {
		err := SandboxRef{Backend: "p", Policy: &SandboxPolicy{MaxAttempts: -1}}.Validate()
		Expect(err).To(MatchError(ContainSubstring("maxAttempts")))
	})
})

var _ = Describe("Spec.Sandbox", func() {
	DescribeTable("survives a Spec marshal round-trip",
		func(marshal func(any) ([]byte, error), decode func([]byte, any) error) {
			spec := Spec{Sandbox: &SandboxRef{Backend: "prod-pool", Agent: "worker-01"}}
			var decoded Spec
			Expect(decode(mustMarshal(marshal, spec), &decoded)).To(Succeed())
			Expect(decoded.Sandbox).To(Equal(spec.Sandbox))
		},
		Entry("JSON", json.Marshal, json.Unmarshal),
		Entry("YAML", yaml.Marshal, yaml.Unmarshal),
	)

	It("replaces wholesale on merge rather than merging key-wise", func() {
		base := Spec{Sandbox: &SandboxRef{Backend: "prod-pool", Agent: "worker-01"}}
		override := Spec{Sandbox: &SandboxRef{Backend: "local-docker"}}

		merged := base.Merge(override)

		Expect(merged.Sandbox).To(Equal(&SandboxRef{Backend: "local-docker"}))
	})

	It("keeps the base sandbox when the override has none", func() {
		base := Spec{Sandbox: &SandboxRef{Backend: "prod-pool"}}

		merged := base.Merge(Spec{})

		Expect(merged.Sandbox).To(Equal(&SandboxRef{Backend: "prod-pool"}))
	})
})

// specMarshal is a hand-maintained mirror of Spec; a field present in one and
// absent from the other silently disappears on marshal. This test turns that
// silent loss into a failure.
var _ = Describe("Spec/specMarshal mirror", func() {
	It("declares the same serialized fields in both structs", func() {
		Expect(jsonTagSet(reflect.TypeOf(Spec{}))).To(
			ConsistOf(jsonTagSet(reflect.TypeOf(specMarshal{}))))
	})
})

func jsonTagSet(t reflect.Type) []string {
	var tags []string
	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		tag, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if tag == "" && field.Anonymous {
			tag = "(inline)" + field.Type.Name()
		}
		tags = append(tags, tag)
	}
	return tags
}

func mustMarshal(marshal func(any) ([]byte, error), v any) []byte {
	encoded, err := marshal(v)
	Expect(err).NotTo(HaveOccurred())
	return encoded
}
