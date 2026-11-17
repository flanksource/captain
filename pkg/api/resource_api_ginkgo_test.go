package api_test

import (
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("API MCP resource policy", func() {
	It("accepts disabled MCP without warnings on API runtimes", func() {
		for _, runtime := range api.AllRuntimes() {
			if runtime.Mode != api.ModeAPI {
				continue
			}
			provider, ok := runtime.ModelProvider()
			Expect(ok).To(BeTrue())
			warnings, err := api.ValidateRuntimeSpec(api.Spec{
				Model:       api.Model{Provider: provider, Mode: api.ModeAPI},
				Permissions: api.Permissions{MCP: api.MCP{Disabled: true}},
			})
			Expect(err).NotTo(HaveOccurred(), runtime.String())
			Expect(warnings).To(BeEmpty(), runtime.String())
			Expect(api.PermissionCapabilitiesFor(runtime).ResourceSupport(api.ResourceKindMCP, api.ResourceEnabled).Kind).
				To(Equal(api.SupportUnsupported), runtime.String())
		}
	})
})
