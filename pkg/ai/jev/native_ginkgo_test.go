package jev

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestJevNative(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Jev Native Suite")
}

var _ = DescribeTable("native request fields", func(raw string) {
	_, err := ParseRequest([]byte(raw))
	Expect(err).To(HaveOccurred())
},
	Entry("rejects unknown top-level fields", `{"model":"jev-latest","state":"Review","questions":{"valid":{"type":"noul","instructions":"Is this valid?"}},"temperature":0.5}`),
	Entry("rejects unknown question fields", `{"model":"jev-latest","state":"Review","questions":{"valid":{"type":"noul","instructions":"Is this valid?","extra":true}}}`),
)
