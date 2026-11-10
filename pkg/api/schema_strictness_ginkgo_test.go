package api_test

import (
	"encoding/json"

	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

var _ = Describe("Schema repair policy", func() {
	DescribeTable("normalizes repair spellings in JSON and YAML", func(value string) {
		var policy api.SchemaStrictness
		Expect(json.Unmarshal([]byte(`"`+value+`"`), &policy)).To(Succeed())
		Expect(policy.Validate()).To(Succeed())
		Expect(string(policy)).To(Equal("repair"))
		encoded, err := json.Marshal(api.SchemaStrictness(value))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(encoded)).To(Equal(`"repair"`))
		Expect(yaml.Unmarshal([]byte(value), &policy)).To(Succeed())
		Expect(string(policy)).To(Equal("repair"))
		encoded, err = yaml.Marshal(api.SchemaStrictness(value))
		Expect(err).NotTo(HaveOccurred())
		Expect(string(encoded)).To(Equal("repair\n"))
	}, Entry("canonical", "repair"), Entry("compatibility alias", "retry"))
})
