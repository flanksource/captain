package claudeagent

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("Claude Agent effort", func() {
	It("carries the requested effort into initialize parameters", func() {
		params, err := (&Provider{}).initializeParams(ai.Request{
			Model: api.Model{Effort: api.EffortHigh},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(params.Effort).To(Equal(api.EffortHigh))

		payload, err := json.Marshal(params)
		Expect(err).NotTo(HaveOccurred())
		var serialized map[string]any
		Expect(json.Unmarshal(payload, &serialized)).To(Succeed())
		Expect(serialized).To(HaveKeyWithValue("effort", "high"))
	})

	It("omits effort when none was requested", func() {
		params, err := (&Provider{}).initializeParams(ai.Request{})
		Expect(err).NotTo(HaveOccurred())
		Expect(params.Effort).To(BeEmpty())

		payload, err := json.Marshal(params)
		Expect(err).NotTo(HaveOccurred())
		var serialized map[string]any
		Expect(json.Unmarshal(payload, &serialized)).To(Succeed())
		Expect(serialized).NotTo(HaveKey("effort"))
	})
})
