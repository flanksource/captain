package provider

import (
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/shell"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Codex app-server permissions", func() {
	It("applies additional workspace roots and safety policy throughout the request lifecycle", func() {
		includeSystemTemp := false
		request := ai.Request{
			Prompt: api.Prompt{User: "inspect"},
			Setup:  &shell.Setup{Cwd: "/repo/worktree"},
			Permissions: api.Permissions{
				Mode:        api.PermissionAuto,
				Directories: []string{"../shared", "/repo/sibling", " ../shared "},
			},
			Sandbox: &api.SandboxRef{Mode: api.SandboxNative, Policy: &api.NativeSandboxPolicy{
				Filesystem: &api.SandboxFilesystemPolicy{
					Access:            api.SandboxFilesystemWorkspaceWrite,
					WritableRoots:     []string{"/sandbox/cache"},
					IncludeSystemTemp: &includeSystemTemp,
				},
				Network: &api.SandboxNetworkPolicy{Access: api.SandboxNetworkUnrestricted},
			}},
		}
		roots := []string{"/repo/worktree", "/repo/shared", "/repo/sibling"}

		start, err := buildThreadStartParams("gpt-5.6", request, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(start).To(HaveKeyWithValue("runtimeWorkspaceRoots", roots))
		Expect(start).To(HaveKeyWithValue("sandbox", "workspace-write"))
		Expect(start).To(HaveKeyWithValue("approvalPolicy", "on-request"))

		resume, err := buildResumeParams(request, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(resume).To(HaveKeyWithValue("runtimeWorkspaceRoots", roots))
		Expect(resume).To(HaveKeyWithValue("sandbox", "workspace-write"))
		Expect(resume).To(HaveKeyWithValue("approvalPolicy", "on-request"))

		turn, err := buildTurnStartParams("gpt-5.6", request, "thread-1", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(turn).To(HaveKeyWithValue("cwd", "/repo/worktree"))
		Expect(turn).To(HaveKeyWithValue("runtimeWorkspaceRoots", roots))
		Expect(turn).To(HaveKeyWithValue("approvalPolicy", "on-request"))
		Expect(turn).To(HaveKeyWithValue("sandboxPolicy", map[string]any{
			"type":                "workspaceWrite",
			"writableRoots":       []string{"/sandbox/cache"},
			"excludeSlashTmp":     true,
			"excludeTmpdirEnvVar": true,
			"networkAccess":       true,
		}))
	})

	It("applies approval posture without requiring a sandbox and maps the bare preset", func() {
		request := ai.Request{
			Prompt: api.Prompt{User: "inspect"},
			Permissions: api.Permissions{
				Mode:    api.PermissionBypass,
				Presets: []api.Preset{api.PresetBare},
			},
		}

		start, err := buildThreadStartParams("gpt-5.6", request, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(start).To(HaveKeyWithValue("approvalPolicy", "never"))
		Expect(start).To(HaveKeyWithValue("ephemeral", true))
		Expect(start).NotTo(HaveKey("sandbox"))
		Expect(start).NotTo(HaveKey("runtimeWorkspaceRoots"))

		resume, err := buildResumeParams(request, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(resume).To(HaveKeyWithValue("approvalPolicy", "never"))
		Expect(resume).NotTo(HaveKey("sandbox"))
		Expect(resume).NotTo(HaveKey("runtimeWorkspaceRoots"))

		turn, err := buildTurnStartParams("gpt-5.6", request, "thread-1", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(turn).To(HaveKeyWithValue("approvalPolicy", "never"))
		Expect(turn).NotTo(HaveKey("sandboxPolicy"))
		Expect(turn).NotTo(HaveKey("runtimeWorkspaceRoots"))
	})

	It("keeps the caller-tool approval timeout from the request", func() {
		provider, err := NewCodexAppServer(ai.Config{CaptainSessionID: "captain-thread-1"})
		Expect(err).NotTo(HaveOccurred())
		options, err := provider.callerToolOptions(ai.Request{
			Permissions: api.Permissions{ApprovalTimeout: "45m"},
		}, []api.ToolDefinition{{Name: "invoice_get"}})

		Expect(err).NotTo(HaveOccurred())
		Expect(options.ApprovalTimeout).To(Equal(45 * time.Minute))
	})
})
