package cli

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ParsePromptArgs", func() {
	It("binds Captain prompt inputs and returns the command's own arguments", func() {
		opts, rest, err := ParsePromptArgs([]string{"--preset", "review", "--var", "change=-", "--vars", `{"language":"go"}`, "--no-presets", "-p", "Review", "pkg", "cmd/x.go"})
		Expect(err).NotTo(HaveOccurred())
		Expect(rest).To(Equal([]string{"pkg", "cmd/x.go"}))
		Expect(opts.Presets).To(Equal([]string{"review"}))
		Expect(opts.Var).To(Equal([]string{"change=-"}))
		Expect(opts.Vars).To(Equal(`{"language":"go"}`))
		Expect(opts.PresetsSet).To(BeTrue())
		Expect(opts.PromptRef).To(Equal("Review"))
		Expect(opts.Prompt).To(Equal("Review"))
	})

	It("marks runtime flags explicit, including false values", func() {
		opts, _, err := ParsePromptArgs([]string{"--model", "agent:sonnet", "--no-cache=false"})
		Expect(err).NotTo(HaveOccurred())
		Expect(opts.Model).To(Equal("agent:sonnet"))
		Expect(opts.ExplicitFields.Has("/model")).To(BeTrue())
		Expect(opts.ExplicitFields.Has("/noCache")).To(BeTrue())
	})

	It("leaves presets unset when neither --preset nor --no-presets is passed", func() {
		opts, rest, err := ParsePromptArgs([]string{"review.prompt"})
		Expect(err).NotTo(HaveOccurred())
		Expect(rest).To(Equal([]string{"review.prompt"}))
		Expect(opts.PresetsSet).To(BeFalse())
		Expect(opts.PromptRef).To(BeEmpty())
	})

	It("keeps each --var whole, so a typed JSON value or a comma in a string is not split", func() {
		vars := []string{`files:=["a.go","b.go"]`, "focus=errors, then style"}
		opts, _, err := ParsePromptArgs([]string{"-V", vars[0], "--var", vars[1]})
		Expect(err).NotTo(HaveOccurred())
		Expect(opts.Var).To(Equal(vars))
	})

	It("fails on a flag Captain does not define", func() {
		_, _, err := ParsePromptArgs([]string{"--not-a-flag"})
		Expect(err).To(MatchError(ContainSubstring("not-a-flag")))
	})
})
