package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"

	"github.com/flanksource/captain/pkg/cli"
	"github.com/flanksource/captain/pkg/runtimeprofiles"
	"github.com/flanksource/clicky/rpc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The runtime preset/profile entities are the webapp's persistence API, so the
// routes the executor derives from them are a contract: the page is written
// against these exact paths and methods.
var _ = Describe("runtime entity routes", func() {
	newExecutorMux := func() *http.ServeMux {
		root := newRootCommand()
		openAPIConfig := &rpc.OpenAPIConfig{Title: "Captain", Description: "test", Version: "test"}
		server := rpc.NewSwaggerServer(&rpc.ServeConfig{
			Title:       openAPIConfig.Title,
			Description: openAPIConfig.Description,
			Version:     openAPIConfig.Version,
			Executor: &rpc.ExecutorConfig{
				Enabled:    true,
				SkipPreRun: true,
				PathPrefix: "/api/v1",
			},
		}, root, openAPIConfig)
		mux := http.NewServeMux()
		server.RegisterExecutionRoutes(mux)
		return mux
	}

	routed := func(mux *http.ServeMux, method, path string) bool {
		_, pattern := mux.Handler(httptest.NewRequest(method, path, nil))
		return pattern != ""
	}

	DescribeTable("publishes every runtime catalog route",
		func(method, path string) {
			Expect(routed(newExecutorMux(), method, path)).To(BeTrue(),
				"%s %s is not served by the executor", method, path)
		},
		Entry("list presets", http.MethodGet, "/api/v1/runtime-preset"),
		Entry("create preset", http.MethodPost, "/api/v1/runtime-preset"),
		Entry("get preset", http.MethodGet, "/api/v1/runtime-preset/some-id"),
		// clicky publishes update at the collection path with the id in the body,
		// exactly as it does for `prompt`; there is no PUT /{id} form.
		Entry("update preset", http.MethodPut, "/api/v1/runtime-preset"),
		Entry("delete preset", http.MethodDelete, "/api/v1/runtime-preset/some-id"),
		Entry("list profiles", http.MethodGet, "/api/v1/runtime-profile"),
		Entry("create profile", http.MethodPost, "/api/v1/runtime-profile"),
		Entry("get profile", http.MethodGet, "/api/v1/runtime-profile/some-id"),
		Entry("update profile", http.MethodPut, "/api/v1/runtime-profile"),
		Entry("delete profile", http.MethodDelete, "/api/v1/runtime-profile/some-id"),
		Entry("resolve profile", http.MethodGet, "/api/v1/runtime-profile/some-id/resolve"),
	)

	// Resolve reads; publishing it as a POST would let a prefetch-safe read be
	// treated as a mutation by every client that keys behaviour on the verb.
	It("serves resolve as a read, not a mutation", func() {
		Expect(routed(newExecutorMux(), http.MethodPost, "/api/v1/runtime-profile/some-id/resolve")).To(BeFalse())
	})

	// The wire shapes the webapp is written against, driven through the real
	// executor over a file-only catalog pinned on the request context.
	Describe("over the executor", func() {
		var (
			handler  http.Handler
			presets  runtimeprofiles.SourceInfo
			profiles runtimeprofiles.SourceInfo
		)

		BeforeEach(func() {
			root := GinkgoT().TempDir()
			presetSource, err := runtimeprofiles.NewFileSource(runtimeprofiles.FileSourceOptions{
				Kind: runtimeprofiles.KindPreset, Dir: filepath.Join(root, "presets"), Label: "team presets", Implicit: true,
			})
			Expect(err).NotTo(HaveOccurred())
			profileSource, err := runtimeprofiles.NewFileSource(runtimeprofiles.FileSourceOptions{
				Kind: runtimeprofiles.KindProfile, Dir: filepath.Join(root, "profiles"), Label: "team profiles", Implicit: true,
			})
			Expect(err).NotTo(HaveOccurred())
			catalog, err := runtimeprofiles.NewCatalog(presetSource, profileSource)
			Expect(err).NotTo(HaveOccurred())
			presets = presetSource.Info()
			profiles = profileSource.Info()
			mux := newExecutorMux()
			handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mux.ServeHTTP(w, r.WithContext(cli.ContextWithRuntimeCatalog(r.Context(), catalog)))
			})
		})

		call := func(method, path string, body any) (int, map[string]any) {
			GinkgoHelper()
			var reader io.Reader
			if body != nil {
				data, err := json.Marshal(body)
				Expect(err).NotTo(HaveOccurred())
				reader = bytes.NewReader(data)
			}
			request := httptest.NewRequest(method, path, reader)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			var decoded map[string]any
			if recorder.Body.Len() > 0 && recorder.Body.Bytes()[0] == '{' {
				Expect(json.Unmarshal(recorder.Body.Bytes(), &decoded)).To(Succeed(), recorder.Body.String())
			}
			return recorder.Code, decoded
		}

		It("creates, lists, updates and resolves through the published routes", func() {
			status, created := call(http.MethodPost, "/api/v1/runtime-preset", map[string]any{
				"target": presets.ID, "name": "Organization", "scope": "global",
				"spec": map[string]any{"model": "claude-sonnet-4-6", "mode": "cli"},
			})
			Expect(status).To(Equal(http.StatusOK), "create: %v", created)
			id, _ := created["id"].(string)
			Expect(id).NotTo(BeEmpty())
			Expect(created["source"]).To(HaveKeyWithValue("kind", "file"))

			request := httptest.NewRequest(http.MethodGet, "/api/v1/runtime-preset", nil)
			request.Header.Set("Accept", "application/json")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			Expect(recorder.Code).To(Equal(http.StatusOK), recorder.Body.String())
			var listed []map[string]any
			Expect(json.Unmarshal(recorder.Body.Bytes(), &listed)).To(Succeed(), "list must be a bare JSON array: %s", recorder.Body.String())
			Expect(listed).To(HaveLen(1))
			Expect(listed[0]).To(HaveKeyWithValue("id", id))
			Expect(listed[0]).To(HaveKey("spec"), "every list item is the whole record")
			Expect(listed[0]).To(HaveKey("source"))

			// clicky publishes update at the collection path and routes it by the
			// id in the body; there is no PUT /{id} form and ?id= is ignored.
			status, renamed := call(http.MethodPut, "/api/v1/runtime-preset", map[string]any{
				"id": id, "name": "Org", "scope": "global",
			})
			Expect(status).To(Equal(http.StatusOK), "update: %v", renamed)
			Expect(renamed).To(HaveKeyWithValue("name", "Org"))
			Expect(renamed).To(HaveKeyWithValue("id", id))

			status, profile := call(http.MethodPost, "/api/v1/runtime-profile", map[string]any{
				"target": profiles.ID, "name": "Review", "presets": []string{id},
			})
			Expect(status).To(Equal(http.StatusOK), "profile create: %v", profile)
			profileID, _ := profile["id"].(string)
			status, resolution := call(http.MethodGet, "/api/v1/runtime-profile/"+url.PathEscape(profileID)+"/resolve", nil)
			Expect(status).To(Equal(http.StatusOK), "resolve: %v", resolution)
			Expect(resolution).To(HaveKey("profile"))
			Expect(resolution).To(HaveKey("resolved"))
			Expect(resolution["presets"]).To(HaveLen(1))
		})
	})
})
