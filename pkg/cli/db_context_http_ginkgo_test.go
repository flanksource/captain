package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("DatabaseContextMiddleware", Serial, func() {
	const secondary = "prod"

	var (
		observed string
		handler  http.Handler
	)

	BeforeEach(func() {
		databaseURLs = nil
		databaseContextFlagValue = ""
		GinkgoT().Setenv("HOME", GinkgoT().TempDir())
		GinkgoT().Setenv(databaseContextEnv, "")
		GinkgoT().Setenv(databaseContextsEnv, secondary+"=postgres://reader@prod/gavel")
		resetDatabaseContextCache()

		observed = ""
		handler = DatabaseContextMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			observed = activeDatabaseContextName(r.Context())
			w.WriteHeader(http.StatusOK)
		}))
	})

	AfterEach(func() { resetDatabaseContextCache() })

	// serve runs one request through the middleware.
	serve := func(request *http.Request) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	decodeError := func(recorder *httptest.ResponseRecorder) databaseContextError {
		var body databaseContextError
		Expect(json.Unmarshal(recorder.Body.Bytes(), &body)).To(Succeed())
		return body
	}

	It("binds the default context when the request selects none", func() {
		recorder := serve(httptest.NewRequest(http.MethodGet, "/api/captain/sessions/live", nil))

		Expect(recorder.Code).To(Equal(http.StatusOK))
		Expect(observed).To(Equal(defaultDatabaseContextName))
	})

	It("binds the context named by the cookie", func() {
		request := httptest.NewRequest(http.MethodGet, "/api/captain/sessions/live", nil)
		request.AddCookie(&http.Cookie{Name: databaseContextCookie, Value: secondary})

		recorder := serve(request)

		Expect(recorder.Code).To(Equal(http.StatusOK))
		Expect(observed).To(Equal(secondary))
	})

	It("prefers the header over the cookie", func() {
		request := httptest.NewRequest(http.MethodGet, "/api/captain/sessions/live", nil)
		request.AddCookie(&http.Cookie{Name: databaseContextCookie, Value: defaultDatabaseContextName})
		request.Header.Set(databaseContextHeader, secondary)

		recorder := serve(request)

		Expect(recorder.Code).To(Equal(http.StatusOK))
		Expect(observed).To(Equal(secondary))
	})

	It("rejects an unknown context with the configured names", func() {
		request := httptest.NewRequest(http.MethodGet, "/api/captain/sessions/live", nil)
		request.Header.Set(databaseContextHeader, "missing")

		recorder := serve(request)

		Expect(recorder.Code).To(Equal(http.StatusBadRequest))
		Expect(decodeError(recorder)).To(Equal(databaseContextError{
			Error:    `unknown database context "missing"`,
			Code:     "unknown_context",
			Contexts: []string{defaultDatabaseContextName, secondary},
		}))
		Expect(observed).To(BeEmpty(), "the request must not reach the handler")
	})

	It("rejects a write against a read-only context", func() {
		request := httptest.NewRequest(http.MethodPost, "/api/chat/sessions", nil)
		request.Header.Set(databaseContextHeader, secondary)

		recorder := serve(request)

		Expect(recorder.Code).To(Equal(http.StatusConflict))
		Expect(decodeError(recorder).Code).To(Equal("read_only_context"))
		Expect(observed).To(BeEmpty(), "the request must not reach the handler")
	})

	It("allows a write against the default context", func() {
		recorder := serve(httptest.NewRequest(http.MethodPost, "/api/chat/sessions", nil))

		Expect(recorder.Code).To(Equal(http.StatusOK))
		Expect(observed).To(Equal(defaultDatabaseContextName))
	})
})
