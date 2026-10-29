package api

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"
)

var _ = Describe("Spec batch", func() {
	DescribeTable("decodes the scalar shorthand and the object form alike",
		func(source string, want Batch) {
			var fromYAML, fromJSON Spec
			Expect(yaml.Unmarshal([]byte(source), &fromYAML)).To(Succeed())
			Expect(*fromYAML.Batch).To(Equal(want))
			raw, err := json.Marshal(fromYAML)
			Expect(err).NotTo(HaveOccurred())
			Expect(json.Unmarshal(raw, &fromJSON)).To(Succeed())
			Expect(*fromJSON.Batch).To(Equal(want))
		},
		Entry("size only", "batch: 1", Batch{Size: 1}),
		Entry("size and concurrency", "batch: {size: 2, concurrency: 4}", Batch{Size: 2, Concurrency: 4}),
	)

	It("lets a later layer's concurrency merge over an earlier size", func() {
		var base, flags Spec
		Expect(yaml.Unmarshal([]byte("batch: 3"), &base)).To(Succeed())
		Expect(yaml.Unmarshal([]byte("batch: {concurrency: 4}"), &flags)).To(Succeed())
		Expect(*base.Merge(flags).Batch).To(Equal(Batch{Size: 3, Concurrency: 4}))
	})

	DescribeTable("refuses a batch that cannot split anything",
		func(batch Batch, want string) {
			Expect(Spec{Batch: &batch}.ValidateStructure()).To(MatchError(ContainSubstring(want)))
		},
		Entry("zero size", Batch{}, "batch.size must be at least 1"),
		Entry("negative concurrency", Batch{Size: 1, Concurrency: -1}, "batch.concurrency must not be negative"),
	)
})
