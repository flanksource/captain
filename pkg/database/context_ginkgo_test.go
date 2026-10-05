package database

import (
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Provider context persistence", func() {
	It("preserves the native percentage without recomputing a token ratio", func() {
		free := 69
		call := turnCallRecord(uuid.New(), IngestModelCall{
			Model: "example-model", InputTokens: 660747, CacheReadTokens: 582144,
			ContextTokens: 89595, ContextWindowTokens: 258400, ContextFreePercent: &free,
		})
		Expect(call.ContextTokens).To(Equal(int64(89595)))
		Expect(call.ContextWindowTokens).To(Equal(int64(258400)))
		Expect(call.ContextFreePercent).To(Equal(&free))
	})
})
