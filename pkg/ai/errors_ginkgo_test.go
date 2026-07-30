package ai

import (
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("model error classification", func() {
	It("does not classify an unsupported Gemini message role as an unavailable model", func() {
		err := errors.New("Error 400, Message: Role 'tool' is not supported. Please use a valid role: SYSTEM, USER, ASSISTANT, MODEL")

		Expect(IsModelUnavailable(err)).To(BeFalse())
		Expect(IsFallbackEligible(err)).To(BeFalse())
	})
})
