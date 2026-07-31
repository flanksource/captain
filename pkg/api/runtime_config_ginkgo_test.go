package api_test

import (
	"github.com/flanksource/captain/pkg/api"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Caller-tool endpoints", func() {
	It("accepts authenticated loopback HTTP and remote HTTPS endpoints", func() {
		for _, endpoint := range []api.CallerToolEndpoint{
			{
				Name: "captain", URL: "http://127.0.0.1:43210/mcp",
				Headers: map[string]string{"Authorization": "Bearer loopback-secret"},
			},
			{
				Name: "captain-remote", URL: "https://captain.example.com/mcp",
				Headers: map[string]string{"authorization": "Bearer remote-secret"},
			},
		} {
			Expect(endpoint.Validate()).To(Succeed())
		}
	})

	It("rejects invalid names, unauthenticated endpoints, and remote plaintext HTTP", func() {
		Expect((api.CallerToolEndpoint{
			Name: "captain tools", URL: "https://captain.example.com/mcp",
			Headers: map[string]string{"Authorization": "Bearer secret"},
		}).Validate()).To(MatchError(ContainSubstring("name")))
		Expect((api.CallerToolEndpoint{
			Name: "captain", URL: "https://captain.example.com/mcp",
		}).Validate()).To(MatchError(ContainSubstring("bearer")))
		Expect((api.CallerToolEndpoint{
			Name: "captain", URL: "http://captain.example.com/mcp",
			Headers: map[string]string{"Authorization": "Bearer secret"},
		}).Validate()).To(MatchError(ContainSubstring("HTTPS")))
	})
})
