package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/commons-db/shell"
	"github.com/flanksource/commons-db/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/pflag"
)

var _ = Describe("captured CLI runtime projection", func() {
	It("rejects a generating invocation without a configured model", func() {
		captainconfig.SetPathForTesting(filepath.Join(GinkgoT().TempDir(), ".captain.yaml"))
		DeferCleanup(captainconfig.SetPathForTesting, "")
		_, err := resolveInvocation(AIRuntimeOptions{}, []api.SpecLayer{api.PromptSpecLayer("generate", api.Spec{Prompt: api.Prompt{User: "Review"}})})
		Expect(err).To(MatchError(ContainSubstring("no model configured")))
	})

	It("renders command-only verification without a configured model", func() {
		captainconfig.SetPathForTesting(filepath.Join(GinkgoT().TempDir(), ".captain.yaml"))
		DeferCleanup(captainconfig.SetPathForTesting, "")
		result, err := renderPrompt(context.Background(), "", PromptRenderRequest{Spec: &api.Spec{Workflow: &api.Workflow{Verify: &api.Verify{Commands: []string{"true"}}}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Input.IsVerifyOnly()).To(BeTrue())
		Expect(result.ValidationError).To(BeEmpty())
		Expect(result.Config.Model.Name).To(BeEmpty())
	})

	It("clears explicitly empty prompt flags after authored and saved defaults", func() {
		opts, err := actionFlagsToOptions(map[string]string{"timeout": "", "system": "", "append-system": ""})
		Expect(err).NotTo(HaveOccurred())
		saved := captainconfig.Config{AI: captainconfig.AIDefaults{Timeout: "2m"}}
		layers, err := renderLoadedLayers(context.Background(), "---\nmodel: agent:claude-sonnet-5\nprompt:\n  system: Authored system\n  appendSystem: Authored suffix\nbudget:\n  timeout: 1m\n---\nReview", "review.prompt", nil, opts, saved)
		Expect(err).NotTo(HaveOccurred())
		result, err := opts.Resolve(AIRuntimeResolveOptions{Layers: layers, Saved: saved})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Request.Prompt.System).To(BeEmpty())
		Expect(result.Request.Prompt.AppendSystem).To(BeEmpty())
		Expect(result.Request.Budget.Timeout).To(BeEmpty())
		for _, path := range []string{"/prompt/system", "/prompt/appendSystem", "/budget/timeout"} {
			Expect(result.Resolution.Provenance[path].Source.Name).To(Equal("prompt flags"))
		}
	})

	It("uses injected prompt source settings without rereading a malformed config file", func() {
		dir := GinkgoT().TempDir()
		path := filepath.Join(dir, ".captain.yaml")
		Expect(os.WriteFile(path, []byte("prompts: [invalid"), 0o600)).To(Succeed())
		captainconfig.SetPathForTesting(path)
		DeferCleanup(captainconfig.SetPathForTesting, "")
		saved := captainconfig.Config{Prompts: captainconfig.PromptDefaults{Dirs: []string{dir}}}
		sources, err := buildPromptSources(context.Background(), promptSourceOptions{Config: &saved})
		Expect(err).NotTo(HaveOccurred())
		resolvedDir, err := filepath.EvalSymlinks(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(sources).To(ContainElement(HaveField("Root", resolvedDir)))
	})

	It("preserves Cobra changed false flags for typed command options", func() {
		flags := pflag.NewFlagSet("runtime", pflag.ContinueOnError)
		flags.Bool("no-cache", false, "")
		flags.Int("max-tokens", 0, "")
		Expect(flags.Parse([]string{"--no-cache=false", "--max-tokens=0"})).To(Succeed())
		opts := (AIRuntimeOptions{}).WithChangedFlags(flags)
		result, err := opts.Resolve(AIRuntimeResolveOptions{Saved: captainconfig.Config{AI: captainconfig.AIDefaults{NoCache: true, MaxTokens: 8000}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Request.NoCache).To(BeFalse())
		Expect(result.Config.NoCache).To(BeFalse())
		Expect(result.Request.Budget.MaxTokens).To(BeZero())
	})

	It("retains authored setup origins while recording path normalization", func() {
		cwd := GinkgoT().TempDir()
		operation := api.Spec{Model: api.Model{Name: "agent:claude-sonnet-5"}, Setup: &shell.Setup{Cwd: "workspace", EnvVars: []types.EnvVar{{Name: "REVIEW_MODE", ValueStatic: "check"}}}}
		result, err := (AIRuntimeOptions{}).Resolve(AIRuntimeResolveOptions{Layers: []api.SpecLayer{api.PromptSpecLayer("operation", operation)}, Cwd: cwd})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Request.Cwd()).To(Equal(filepath.Join(cwd, "workspace")))
		Expect(result.Request.Setup.EnvVars).To(Equal(operation.Setup.EnvVars))
		Expect(result.Resolution.Provenance["/setup/envVars/0/value"].Source.Name).To(Equal("operation"))
		Expect(result.Resolution.Provenance["/setup/envVars/0/value"].NormalizedBy).To(BeNil())
		Expect(result.Resolution.Provenance["/setup/cwd"].Source.Name).To(Equal("operation"))
		Expect(result.Resolution.Provenance["/setup/cwd"].NormalizedBy).To(HaveField("Name", "runtime context"))
		Expect(result.Resolution.Trace[0].Spec.Setup.Cwd).To(Equal("workspace"))
	})

	It("projects an already resolved spec without reapplying flags or saved defaults", func() {
		input := api.ResolvedSpec{Spec: api.Spec{Model: api.Model{Name: "claude-sonnet-5", Mode: api.ModeAgent}, Budget: api.Budget{Cost: 2}}, Warnings: []string{"declared warning"}}
		opts := AIRuntimeOptions{AIProviderOptions: AIProviderOptions{Budget: "3"}}
		result, err := opts.Project(AIRuntimeProjectOptions{Resolved: input, Saved: captainconfig.Config{AI: captainconfig.AIDefaults{BudgetUSD: 1, NoCache: true}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Resolution).To(Equal(input))
		Expect(result.Request).To(Equal(input.Spec))
		Expect(result.Config.Budget).To(Equal(input.Spec.Budget))
		Expect(result.Config.NoCache).To(BeFalse())
	})

	It("lets explicit false and zero CLI flags override saved settings", func() {
		opts, err := actionFlagsToOptions(map[string]string{"model": "agent:claude-sonnet-5", "no-cache": "false", "no-hooks": "false", "max-tokens": "0", "temperature": "0"})
		Expect(err).NotTo(HaveOccurred())
		result, err := opts.Resolve(AIRuntimeResolveOptions{Saved: captainconfig.Config{AI: captainconfig.AIDefaults{NoCache: true, NoHooks: true, MaxTokens: 8000}}})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Request.NoCache).To(BeFalse())
		Expect(result.Request.Memory.SkipHooks).To(BeFalse())
		Expect(result.Request.Budget.MaxTokens).To(BeZero())
		Expect(result.Request.Temperature).To(HaveValue(BeZero()))
	})

	It("preserves the complete operation and authored false values in matching request and config", func() {
		cwd := GinkgoT().TempDir()
		operation := api.Spec{
			Model:       api.Model{Name: "claude-sonnet-5", Mode: api.ModeAgent},
			Budget:      api.Budget{Cost: 2, MaxTokens: 512, Timeout: "1m"},
			Prompt:      api.Prompt{User: "Review the change", SchemaJSON: json.RawMessage(`{"type":"object"}`)},
			Permissions: api.Permissions{Mode: api.PermissionAcceptEdits, Tools: api.Tools{"Read": api.ToolPolicyAllow}},
			Memory:      api.Memory{Skills: []string{"review-skills"}},
			Setup:       &shell.Setup{Env: []string{"REVIEW_MODE=check"}},
			SessionID:   "review-session",
			CLIArgs:     map[string]any{"review": true},
		}.WithExplicit("/noCache", "/memory/skipHooks", "/permissions/mcp/disabled")
		saved := captainconfig.Config{AI: captainconfig.AIDefaults{BudgetUSD: 1, NoCache: true, NoHooks: true, NoMCP: true}}
		path := filepath.Join(cwd, ".captain.yaml")
		Expect(os.WriteFile(path, []byte("ai: [invalid"), 0o600)).To(Succeed())
		captainconfig.SetPathForTesting(path)
		DeferCleanup(captainconfig.SetPathForTesting, "")
		opts := AIRuntimeOptions{AIProviderOptions: AIProviderOptions{APIKey: "example", APIURL: "http://127.0.0.1:9911"}}

		resolved, err := opts.Resolve(AIRuntimeResolveOptions{Layers: []api.SpecLayer{api.PromptSpecLayer("operation", operation)}, Saved: saved, Cwd: cwd, RequireModel: true})

		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Request.Budget).To(Equal(operation.Budget))
		Expect(resolved.Config.Budget).To(Equal(operation.Budget))
		Expect(resolved.Config.Model).To(Equal(resolved.Request.Model))
		Expect(resolved.Request.NoCache).To(BeFalse())
		Expect(resolved.Config.NoCache).To(BeFalse())
		Expect(resolved.Request.Memory).To(Equal(operation.Memory))
		Expect(resolved.Request.Permissions).To(Equal(operation.Permissions))
		Expect(resolved.Request.Prompt).To(Equal(operation.Prompt))
		Expect(resolved.Request.CLIArgs).To(Equal(operation.CLIArgs))
		Expect(resolved.Request.SessionID).To(Equal(operation.SessionID))
		Expect(resolved.Config.SessionID).To(Equal(operation.SessionID))
		Expect(resolved.Request.Setup.Env).To(Equal(operation.Setup.Env))
		Expect(resolved.Request.Cwd()).To(Equal(cwd))
		Expect(resolved.Config.APIKey).To(Equal("example"))
		Expect(resolved.Config.APIURL).To(Equal("http://127.0.0.1:9911"))
		Expect(resolved.Resolution.Spec).To(Equal(resolved.Request))
	})

	It("resolves a named sandbox from the injected full settings snapshot", func() {
		operation := api.Spec{Model: api.Model{Name: "claude-sonnet-5", Mode: api.ModeCLI}, Sandbox: &api.SandboxRef{Backend: "review-pool"}}
		saved := captainconfig.Config{Sandbox: captainconfig.SandboxDefaults{Backends: map[string]captainconfig.SandboxBackend{
			"review-pool": {Kind: "docker", Options: map[string]any{"image": "example/review"}},
		}}}
		resolved, err := (AIRuntimeOptions{}).Resolve(AIRuntimeResolveOptions{Layers: []api.SpecLayer{api.PromptSpecLayer("operation", operation)}, Saved: saved, Cwd: GinkgoT().TempDir(), RequireModel: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Config.SandboxSelection).To(Equal(&api.SandboxConfig{Kind: api.SandboxDocker, Name: "review-pool", Options: map[string]any{"image": "example/review"}}))
		Expect(resolved.Request.Sandbox.Backend).To(Equal("review-pool"))
	})

	It("retains an explicit sandbox selector in the CLI layer before backend normalization", func() {
		opts := AIRuntimeOptions{AIProviderOptions: AIProviderOptions{Sandbox: "review-pool"}}
		saved := captainconfig.Config{Sandbox: captainconfig.SandboxDefaults{Backends: map[string]captainconfig.SandboxBackend{
			"review-pool": {Kind: "docker"},
		}}}
		result, err := opts.Resolve(AIRuntimeResolveOptions{Layers: []api.SpecLayer{api.PromptSpecLayer("operation", api.Spec{Model: api.Model{Name: "claude-sonnet-5", Mode: api.ModeCLI}})}, Saved: saved})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Resolution.Trace[len(result.Resolution.Trace)-1].Spec.Sandbox).To(Equal(&api.SandboxRef{Backend: "review-pool"}))
		Expect(result.Resolution.Provenance["/sandbox/backend"].Source.Name).To(Equal("CLI flags"))
		Expect(result.Request.Sandbox.Mode).To(Equal(api.SandboxDocker))
	})

	It("keeps an explicit sandbox null from restoring the saved default during resolution or projection", func() {
		spec := api.Spec{Model: api.Model{Name: "claude-sonnet-5", Mode: api.ModeCLI}}.WithExplicit("/sandbox")
		saved := captainconfig.Config{Sandbox: captainconfig.SandboxDefaults{Default: "docker"}}
		result, err := (AIRuntimeOptions{}).Resolve(AIRuntimeResolveOptions{Layers: []api.SpecLayer{api.RequestSpecLayer("clear sandbox", spec)}, Saved: saved})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Request.Sandbox).To(BeNil())
		Expect(result.Config.SandboxSelection).To(BeNil())
		Expect(result.Resolution.Provenance["/sandbox"].Source.Name).To(Equal("clear sandbox"))
	})

	It("returns final capability warnings for the complete authored request", func() {
		operation := api.Spec{Model: api.Model{Name: "claude-sonnet-5", Mode: api.ModeAgent}, Permissions: api.Permissions{Plugins: api.ResourcePolicies{"review-tools": api.ResourceEnabled}}}
		resolved, err := (AIRuntimeOptions{}).Resolve(AIRuntimeResolveOptions{Layers: []api.SpecLayer{api.PromptSpecLayer("operation", operation)}, Cwd: GinkgoT().TempDir(), RequireModel: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Resolution.Warnings).To(HaveExactElements(ContainSubstring("plugins")))
	})
})
