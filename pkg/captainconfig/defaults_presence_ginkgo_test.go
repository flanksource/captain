package captainconfig

import (
	"encoding/json"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

func TestSavedDefaultsPresence(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Saved defaults presence")
}

var _ = Describe("saved defaults field presence", func() {
	DescribeTable("roundtrips explicit global zero values", func(input string, decode func([]byte, any) error, encode func(any) ([]byte, error)) {
		var saved AIDefaults
		Expect(decode([]byte(input), &saved)).To(Succeed())
		for _, path := range []string{"/temperature", "/budgetUSD", "/maxTokens", "/noCache", "/noMCP"} {
			Expect(saved.Explicit).To(HaveKeyWithValue(path, true))
		}
		encoded, err := encode(saved)
		Expect(err).NotTo(HaveOccurred())
		var roundtrip AIDefaults
		Expect(decode(encoded, &roundtrip)).To(Succeed())
		Expect(roundtrip).To(Equal(saved))
	},
		Entry("JSON", `{"temperature":0,"budgetUSD":0,"maxTokens":0,"noCache":false,"noMCP":false}`, json.Unmarshal, json.Marshal),
		Entry("YAML", "temperature: 0\nbudgetUSD: 0\nmaxTokens: 0\nnoCache: false\nnoMCP: false\n", yaml.Unmarshal, yaml.Marshal),
	)

	It("leaves absent generation settings unset", func() {
		var saved AIDefaults
		Expect(yaml.Unmarshal([]byte("defaultModel: agent:sonnet:high\n"), &saved)).To(Succeed())
		Expect(saved.Explicit).NotTo(HaveKey("/temperature"))
		Expect(saved.Explicit).NotTo(HaveKey("/noCache"))
	})

	It("tracks zero values from YAML merged defaults", func() {
		var saved AIDefaults
		Expect(yaml.Unmarshal([]byte("<<: &defaults {noCache: false, temperature: 0}\ndefaultModel: agent:sonnet\n"), &saved)).To(Succeed())
		Expect(saved.Explicit).To(HaveKeyWithValue("/noCache", true))
		Expect(saved.Explicit).To(HaveKeyWithValue("/temperature", true))
		Expect(saved.Explicit).NotTo(HaveKey("/<<"))
	})
})
