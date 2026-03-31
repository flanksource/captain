package collections

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestCollections(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Collections Suite")
}

var _ = Describe("FindSimilar", func() {
	DescribeTable("returns closest matches",
		func(target string, candidates []string, topN int, expected []string) {
			result := FindSimilar(target, candidates, topN)
			Expect(result).To(Equal(expected))
		},
		Entry("typo in tool name", "Grepp", []string{"Grep", "Glob", "Read", "Write"}, 2, []string{"Grep", "Read"}),
		Entry("case insensitive", "grep", []string{"Grep", "Glob", "Read"}, 1, []string{"Grep"}),
		Entry("empty candidates", "test", []string{}, 3, nil),
		Entry("topN larger than candidates", "test", []string{"best", "rest"}, 5, []string{"best", "rest"}),
		Entry("exact match first", "Bash", []string{"Read", "Bash", "Write"}, 1, []string{"Bash"}),
	)

	DescribeTable("Levenshtein distance",
		func(s1, s2 string, expected int) {
			Expect(Levenshtein(s1, s2)).To(Equal(expected))
		},
		Entry("identical", "abc", "abc", 0),
		Entry("one insertion", "abc", "abcd", 1),
		Entry("one deletion", "abcd", "abc", 1),
		Entry("one substitution", "abc", "aXc", 1),
		Entry("empty first", "", "abc", 3),
		Entry("empty second", "abc", "", 3),
		Entry("both empty", "", "", 0),
	)
})
