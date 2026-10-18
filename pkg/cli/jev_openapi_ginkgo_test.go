package cli

import (
	"github.com/flanksource/clicky/rpc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = DescribeTable("Jev provider OpenAPI paths include the provider selector", func(path string, addPaths func(*rpc.OpenAPISpec)) {
	spec := &rpc.OpenAPISpec{}
	addPaths(spec)
	Expect(spec.Paths).To(HaveKey(path))
	for _, operation := range spec.Paths[path] {
		providerFound := false
		for _, parameter := range operation.Parameters {
			if parameter.Name == "provider" {
				providerFound = true
				Expect(parameter.Schema.Enum).To(ContainElement("jev"))
			}
		}
		Expect(providerFound).To(BeTrue())
	}
},
	Entry("token management", "/api/captain/ai/providers/{provider}/token", addCaptainProviderTokenPaths),
	Entry("runtime defaults", "/api/captain/ai/providers/{provider}/defaults", addCaptainProviderDefaultsPaths),
)
