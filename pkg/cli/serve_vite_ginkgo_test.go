package cli

import (
	"io"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Vite development proxy", func() {
	It("serves workspace-backed frontend paths through Captain's port", func() {
		vite := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Vite-Request-URI", r.URL.RequestURI())
			w.WriteHeader(http.StatusAccepted)
			_, err := io.WriteString(w, "vite response")
			Expect(err).NotTo(HaveOccurred())
		}))
		DeferCleanup(vite.Close)

		handler, err := newCaptainUIHandler(captainUIHandlerOptions{
			Dev:     true,
			ViteURL: vite.URL,
		})
		Expect(err).NotTo(HaveOccurred())
		request := httptest.NewRequest(http.MethodGet, "/@vite/client?direct=true", nil)
		response := httptest.NewRecorder()

		handler.ServeHTTP(response, request)

		Expect(response.Code).To(Equal(http.StatusAccepted))
		Expect(response.Header().Get("X-Vite-Request-URI")).To(Equal("/@vite/client?direct=true"))
		Expect(response.Body.String()).To(Equal("vite response"))
	})

	It("rejects development mode without a Vite target", func() {
		_, err := newCaptainUIHandler(captainUIHandlerOptions{Dev: true})

		Expect(err).To(MatchError("vite development URL is required"))
	})
})
