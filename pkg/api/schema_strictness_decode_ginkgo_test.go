package api_test

import (
	"encoding/json"

	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

var _ = Describe("Schema strictness decoding", func() {
	It("rejects an unknown policy when decoding JSON and YAML", func() {
		const want = `invalid schemaStrictness "strict"; want one of: none, warning, error, repair (retry is an alias)`
		var policy api.SchemaStrictness
		Expect(json.Unmarshal([]byte(`"strict"`), &policy)).To(MatchError(want))
		Expect(yaml.Unmarshal([]byte("strict"), &policy)).To(MatchError(want))
		Expect(policy).To(BeEmpty())
	})
})
