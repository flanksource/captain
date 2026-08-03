package anthropicmock

import (
	"net/http"

	"github.com/flanksource/captain/pkg/aimock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Anthropic mock health probe", func() {
	It("records the Claude SDK root HEAD probe without a miss", func() {
		scenario, err := aimock.Parse([]byte(`
anthropic:
  - respond: {text: unused}
`))
		Expect(err).NotTo(HaveOccurred())
		server, err := Start(Options{Scenario: scenario})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(server.Close)

		request, err := http.NewRequest(http.MethodHead, server.URL()+"/", nil)
		Expect(err).NotTo(HaveOccurred())
		response, err := http.DefaultClient.Do(request)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(response.Body.Close)

		Expect(response.StatusCode).To(Equal(http.StatusOK))
		Expect(server.Requests()).To(HaveLen(1))
		Expect(server.Requests()[0].Miss).To(BeEmpty())
	})
})
