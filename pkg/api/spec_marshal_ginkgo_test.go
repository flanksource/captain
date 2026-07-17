package api

import (
	"encoding/json"

	"github.com/flanksource/commons-db/shell"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

var _ = Describe("Spec serialization", func() {
	DescribeTable("omits empty strings and sections",
		func(marshal func(any) ([]byte, error), decode func([]byte, any) error) {
			encoded, err := marshal(Spec{
				Memory:      Memory{Skills: []string{}},
				Permissions: Permissions{Tools: Tools{Modes: map[string]ToolMode{}}},
				Setup:       &shell.Setup{},
				Workflow:    &Workflow{},
			})
			Expect(err).NotTo(HaveOccurred())

			var decoded map[string]any
			Expect(decode(encoded, &decoded)).To(Succeed())
			Expect(decoded).To(BeEmpty())
		},
		Entry("JSON", json.Marshal, json.Unmarshal),
		Entry("YAML", yaml.Marshal, yaml.Unmarshal),
	)

	DescribeTable("preserves non-empty values while omitting empty neighbors",
		func(marshal func(any) ([]byte, error), decode func([]byte, any) error) {
			encoded, err := marshal(Spec{Model: Model{Name: "opus"}})
			Expect(err).NotTo(HaveOccurred())

			var decoded map[string]any
			Expect(decode(encoded, &decoded)).To(Succeed())
			Expect(decoded).To(Equal(map[string]any{"model": "opus"}))
		},
		Entry("JSON", json.Marshal, json.Unmarshal),
		Entry("YAML", yaml.Marshal, yaml.Unmarshal),
	)
})
