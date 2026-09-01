package provider

import (
	"encoding/json"
	"os"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("provider-neutral sandbox translation", func() {
	It("writes one Claude settings document with native isolation and approval", func() {
		required := true
		allowLocalBinding := false
		req := ai.Request{Permissions: api.Permissions{Mode: api.PermissionPlan}, Sandbox: &api.SandboxRef{
			Mode: api.SandboxNative,
			Policy: &api.NativeSandboxPolicy{
				Required: &required,
				Filesystem: &api.SandboxFilesystemPolicy{
					Access:          api.SandboxFilesystemWorkspaceWrite,
					WritableRoots:   []string{"/workspace/cache"},
					DeniedReadRoots: []string{"~/.ssh"},
				},
				Network: &api.SandboxNetworkPolicy{
					Access:            api.SandboxNetworkRestricted,
					AllowedDomains:    []string{"registry.example.com"},
					AllowLocalBinding: &allowLocalBinding,
				},
			},
		}}

		args, cleanup, err := buildClaudeCLIArgs("claude-sonnet-5", req)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(cleanup)
		Expect(sandboxFlagValue(args, "--permission-mode")).To(Equal("plan"))

		data, err := os.ReadFile(sandboxFlagValue(args, "--settings"))
		Expect(err).NotTo(HaveOccurred())
		var settings map[string]any
		Expect(json.Unmarshal(data, &settings)).To(Succeed())
		Expect(settings["sandbox"]).To(Equal(map[string]any{
			"enabled":           true,
			"failIfUnavailable": true,
			"filesystem": map[string]any{
				"allowWrite": []any{"/workspace/cache"},
				"denyRead":   []any{"~/.ssh"},
			},
			"network": map[string]any{
				"allowedDomains":    []any{"registry.example.com"},
				"allowLocalBinding": false,
			},
		}))
	})

	It("maps the same native policy to Codex sandbox and strict config", func() {
		includeSystemTemp := false
		req := ai.Request{Permissions: api.Permissions{Mode: api.PermissionAuto}, Sandbox: &api.SandboxRef{
			Mode: api.SandboxNative,
			Policy: &api.NativeSandboxPolicy{
				Filesystem: &api.SandboxFilesystemPolicy{
					Access:            api.SandboxFilesystemWorkspaceWrite,
					WritableRoots:     []string{"/workspace/cache"},
					IncludeSystemTemp: &includeSystemTemp,
				},
				Network: &api.SandboxNetworkPolicy{Access: api.SandboxNetworkUnrestricted},
			},
		}}

		args, cleanup, err := buildCodexCLIArgs(codexCLIConfig{Model: "gpt-5.5"}, req)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(cleanup)
		Expect(sandboxFlagValue(args, "--sandbox")).To(Equal("workspace-write"))
		Expect(args).To(ContainElements(
			"approval_policy=\"on-request\"",
			"sandbox_workspace_write.network_access=true",
			"sandbox_workspace_write.writable_roots=[\"/workspace/cache\"]",
			"sandbox_workspace_write.exclude_slash_tmp=true",
			"sandbox_workspace_write.exclude_tmpdir_env_var=true",
		))
	})

	It("rejects a native field the active provider cannot translate", func() {
		req := ai.Request{Sandbox: &api.SandboxRef{
			Mode: api.SandboxNative,
			Policy: &api.NativeSandboxPolicy{Network: &api.SandboxNetworkPolicy{
				Access: api.SandboxNetworkRestricted, AllowedDomains: []string{"example.com"},
			}},
		}}
		_, cleanup, err := buildCodexCLIArgs(codexCLIConfig{Model: "gpt-5.5"}, req)
		DeferCleanup(cleanup)
		Expect(err).To(MatchError(ContainSubstring("sandbox.policy.network.allowedDomains is not supported by openai cli")))
	})
})

func sandboxFlagValue(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}
