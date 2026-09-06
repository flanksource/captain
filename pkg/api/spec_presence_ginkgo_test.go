package api

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

var _ = Describe("Spec field presence", func() {
	DescribeTable("preserves false zero and empty values through the full wire", func(decode func([]byte, any) error, encode func(any) ([]byte, error), data string) {
		var spec Spec
		Expect(decode([]byte(data), &spec)).To(Succeed())
		Expect(spec.Prompt.User).To(Equal("review"))
		Expect(spec.Budget.MaxTurns).To(Equal(4))
		Expect(spec.Fields()).To(HaveKeyWithValue("/noCache", true))
		Expect(spec.Fields()).To(HaveKeyWithValue("/budget/cost", true))
		Expect(spec.Fields()).To(HaveKeyWithValue("/fallbacks", true))
		Expect(spec.Fields()).To(HaveKeyWithValue("/memory/skipSkills", true))
		encoded, err := encode(spec)
		Expect(err).NotTo(HaveOccurred())
		var again Spec
		Expect(decode(encoded, &again)).To(Succeed())
		Expect(again).To(Equal(spec))
	},
		Entry("JSON", json.Unmarshal, json.Marshal, `{"model":"agent:sonnet","noCache":false,"fallbacks":[],"budget":{"cost":0,"maxTurns":4},"memory":{"skipSkills":false},"prompt":{"user":"review"}}`),
		Entry("YAML", yaml.Unmarshal, yaml.Marshal, "model: agent:sonnet\nnoCache: false\nfallbacks: []\nbudget: {cost: 0, maxTurns: 4}\nmemory: {skipSkills: false}\nprompt: review\n"),
	)

	It("merges intentional zero values without losing sibling fields or mutating input", func() {
		base := Spec{Model: Model{Name: "sonnet", NoCache: true, Fallbacks: []Model{{Name: "sol"}}}, Budget: Budget{Cost: 2, MaxTurns: 4}, Memory: Memory{SkipSkills: true}}
		override := (Spec{}).WithExplicit("/noCache", "/fallbacks", "/budget/cost", "/memory/skipSkills")
		merged := base.Merge(override)
		Expect(merged.NoCache).To(BeFalse())
		Expect(merged.Fallbacks).To(BeEmpty())
		Expect(merged.Budget).To(Equal(Budget{MaxTurns: 4}))
		Expect(merged.Memory.SkipSkills).To(BeFalse())
		Expect(base.NoCache).To(BeTrue())
		Expect(base.Fallbacks).To(HaveLen(1))
		Expect(IsEmpty(override)).To(BeFalse())
	})

	It("removes session presence when deriving an independent run", func() {
		spec := (Spec{SessionID: "old-session"}).WithExplicit("/sessionId", "/messages", "/toolApproval")
		independent := spec.WithoutSession()
		Expect(independent.Fields()).NotTo(HaveKey("/sessionId"))
		Expect(independent.Fields()).NotTo(HaveKey("/messages"))
		Expect(independent.Fields()).NotTo(HaveKey("/toolApproval"))
		Expect(IsEmpty(independent)).To(BeTrue())
	})

	It("replaces fallback lists without retaining the replaced entries' presence", func() {
		var base Spec
		Expect(json.Unmarshal([]byte(`{"fallbacks":[{"model":"sonnet","noCache":false},{"model":"sol","noCache":false}]}`), &base)).To(Succeed())
		merged := base.Merge(Spec{Model: Model{Fallbacks: []Model{{Name: "haiku"}}}})
		encoded, err := json.Marshal(merged)
		Expect(err).NotTo(HaveOccurred())
		var again Spec
		Expect(json.Unmarshal(encoded, &again)).To(Succeed())
		Expect(again.Fallbacks).To(HaveLen(1))
		Expect(again.Fields()).NotTo(HaveKey("/fallbacks/1/noCache"))
		Expect(again.Fallbacks[0].Fields()).NotTo(HaveKey("/noCache"))
	})

	DescribeTable("delegates unknown field policy to the enclosing decoder", func(decode func([]byte, any) error, data string) {
		var spec Spec
		Expect(decode([]byte(data), &spec)).To(Succeed())
		encoded, err := json.Marshal(spec)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(encoded)).NotTo(ContainSubstring("unexpected"))
	},
		Entry("JSON budget", json.Unmarshal, `{"budget":{"unexpected":0}}`),
		Entry("JSON fallback", json.Unmarshal, `{"fallbacks":[{"model":"sonnet","unexpected":1}]}`),
		Entry("YAML budget", yaml.Unmarshal, "budget: {unexpected: 0}"),
		Entry("YAML fallback", yaml.Unmarshal, "fallbacks: [{model: sonnet, unexpected: 1}]"),
	)

	It("preserves reusable preset groups and zero presence through both wire formats", func() {
		for _, encoding := range []struct {
			Decode func([]byte, any) error
			Encode func(any) ([]byte, error)
			Input  string
		}{
			{json.Unmarshal, json.Marshal, `{"model":"sonnet","budget":{"cost":0,"maxTurns":3},"memory":{"skipSkills":false},"setup":{"checkout":{"worktree":{"keep":false}}}}`},
			{yaml.Unmarshal, yaml.Marshal, "model: sonnet\nbudget: {cost: 0, maxTurns: 3}\nmemory: {skipSkills: false}\nsetup: {checkout: {worktree: {keep: false}}}"},
		} {
			var preset RuntimePresetSpec
			Expect(encoding.Decode([]byte(encoding.Input), &preset)).To(Succeed())
			Expect(preset.Budget.MaxTurns).To(Equal(3))
			Expect(preset.ToSpec().Fields()).To(HaveKey("/budget/cost"))
			Expect(preset.ToSpec().Fields()).To(HaveKey("/setup/checkout/worktree/keep"))
			encoded, err := encoding.Encode(preset)
			Expect(err).NotTo(HaveOccurred())
			var again RuntimePresetSpec
			Expect(encoding.Decode(encoded, &again)).To(Succeed())
			Expect(again).To(Equal(preset))
		}
	})

	DescribeTable("preserves exact numeric values inside raw structured-output schemas", func(encode func(any) ([]byte, error), decode func([]byte, any) error) {
		schema := json.RawMessage(`{"const":9007199254740993,"additionalProperties":false,"minimum":0}`)
		spec := Spec{Prompt: Prompt{User: "return the constant", SchemaJSON: schema}}
		encoded, err := encode(spec)
		Expect(err).NotTo(HaveOccurred())
		var again Spec
		Expect(decode(encoded, &again)).To(Succeed())
		Expect(again.Prompt.SchemaJSON).To(MatchJSON(schema))
		encoded, err = json.Marshal(again)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(encoded)).To(ContainSubstring(`9007199254740993`))
	}, Entry("JSON", json.Marshal, json.Unmarshal), Entry("YAML", yaml.Marshal, yaml.Unmarshal))
})
