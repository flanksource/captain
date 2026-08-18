package main

import (
	"net/http"
	"net/http/httptest"

	"github.com/flanksource/clicky/rpc"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The REST executor publishes every cobra command as an unauthenticated route
// under /api/v1. Commands that administer the host rather than serve a resource
// must stay off it: `git-agent serve` would block the server on an SSH listener,
// `hook` runs hook sets against a caller-chosen repo, `run-task` launches an
// agent in an arbitrary worktree, `ssh` exits the process, `add` mints a join
// token, and `serve` starts a nested server.
//
// Cobra's Hidden flag does NOT exclude a command from the executor — only the
// clicky local-only annotation does — so `hook`, `run-task` and `ssh` were all
// reachable despite being hidden. This pins the exclusion at the route level
// rather than trusting the annotation, so it still fails if clicky changes how
// the executor filters commands.
var _ = Describe("REST executor exposure", func() {
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

	// routed reports whether the mux has a real handler for method+path, as
	// opposed to falling through to net/http's NotFoundHandler.
	routed := func(mux *http.ServeMux, method, path string) bool {
		_, pattern := mux.Handler(httptest.NewRequest(method, path, nil))
		return pattern != ""
	}

	DescribeTable("host-administering commands are not routable",
		func(method, path string) {
			Expect(routed(newExecutorMux(), method, path)).To(BeFalse(),
				"%s %s is published as an unauthenticated REST route", method, path)
		},
		Entry("git-agent serve", http.MethodPost, "/api/v1/sandbox/git-agent/serve"),
		Entry("git-agent hook", http.MethodPost, "/api/v1/sandbox/git-agent/hook"),
		Entry("git-agent run-task", http.MethodPost, "/api/v1/sandbox/git-agent/run-task"),
		Entry("git-agent ssh", http.MethodPost, "/api/v1/sandbox/git-agent/ssh"),
		Entry("git-agent add", http.MethodPost, "/api/v1/sandbox/git-agent"),
		Entry("git-agent list", http.MethodGet, "/api/v1/sandbox/git-agent"),
		Entry("captain serve", http.MethodPost, "/api/v1/serve"),
	)

	// The token group is the load-bearing case: these routes are what stands in
	// front of an off-box caller, so publishing `create` unauthenticated would
	// let anyone who could already reach the API mint themselves a durable
	// credential. Every method is probed rather than the one the command maps
	// to, so a change in how clicky derives verbs cannot quietly open a route.
	It("publishes no token route under any method", func() {
		mux := newExecutorMux()
		for _, path := range []string{
			"/api/v1/token", "/api/v1/token/create", "/api/v1/token/list",
			"/api/v1/token/revoke", "/api/v1/token/some-token-id",
		} {
			for _, method := range []string{
				http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete,
			} {
				Expect(routed(mux, method, path)).To(BeFalse(),
					"%s %s would let a caller mint or revoke a credential over the API it authenticates", method, path)
			}
		}
	})

	// A guard on the guard: if the executor stopped registering anything at all
	// the table above would pass vacuously.
	It("still routes ordinary resource commands", func() {
		mux := newExecutorMux()
		Expect(routed(mux, http.MethodGet, "/api/v1/sessions")).To(BeTrue(),
			"executor registered no routes at all; the exclusion table proves nothing")
	})
})
