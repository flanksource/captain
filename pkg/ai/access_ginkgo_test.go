package ai

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

type accessCaptureProvider struct {
	runtime Runtime
	err     error
	request Request
}

func (p *accessCaptureProvider) Execute(_ context.Context, req Request) (*Response, error) {
	p.request = req
	return &Response{}, p.err
}

func (p *accessCaptureProvider) GetModel() string    { return "capture" }
func (p *accessCaptureProvider) GetRuntime() Runtime { return p.runtime }

var _ = Describe("access decorator", func() {
	It("compiles read-only for each fallback candidate in its own dialect", func() {
		claude := &accessCaptureProvider{runtime: RuntimeOf(Anthropic, ModeAgent), err: errors.New("anthropic: 429 rate limit")}
		codex := &accessCaptureProvider{runtime: RuntimeOf(OpenAI, ModeAgent)}
		byName := map[string]Provider{"sonnet": withAccess(claude), "gpt": withAccess(codex)}
		fallback := &fallbackProvider{
			candidates: []api.Model{{Name: "sonnet"}, {Name: "gpt"}},
			build:      func(cfg Config) (Provider, error) { return byName[cfg.Model.Name], nil },
			built:      make([]Provider, 2),
		}
		req := Request{Permissions: api.Permissions{Access: api.AccessReadOnly}}

		_, err := fallback.Execute(context.Background(), req)

		Expect(err).NotTo(HaveOccurred())
		Expect(claude.request.Permissions.Tools.DenyList()).To(ContainElements("Bash", "Edit", "Write", "Agent"))
		Expect(claude.request.Sandbox).To(BeNil())
		Expect(codex.request.Permissions.Tools).To(BeEmpty())
		Expect(codex.request.Sandbox.Policy.Filesystem.Access).To(Equal(api.SandboxFilesystemReadOnly))
		Expect(codex.request.Permissions.Mode).To(Equal(api.PermissionBypass))
		Expect(req.Permissions.Tools).To(BeNil(), "the caller's request is never written through")
	})

	It("passes a request without access through unchanged", func() {
		capture := &accessCaptureProvider{runtime: RuntimeOf(OpenAI, ModeCLI)}
		req := Request{Permissions: api.Permissions{Mode: api.PermissionPlan, Tools: api.Tools{"Read": api.ToolPolicyAllow}}}

		_, err := withAccess(capture).Execute(context.Background(), req)

		Expect(err).NotTo(HaveOccurred())
		Expect(capture.request).To(Equal(req))
	})

	It("refuses a candidate that cannot reach read-only before it runs", func() {
		capture := &accessCaptureProvider{runtime: RuntimeOf(OpenAI, ModeAgent)}
		req := Request{Permissions: api.Permissions{Access: api.AccessReadOnly}, Sandbox: &api.SandboxRef{Mode: api.SandboxOff}}

		_, err := withAccess(capture).Execute(context.Background(), req)

		Expect(err).To(MatchError(ContainSubstring(`sandbox mode "off" cannot confine the agent`)))
		Expect(capture.request).To(Equal(Request{}))
	})
})
