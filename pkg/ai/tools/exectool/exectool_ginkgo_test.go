package exectool_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/commons-db/shell"
	"github.com/flanksource/commons-db/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/ai/tools/exectool"
	"github.com/flanksource/captain/pkg/api"
)

// countingScript records each attempt in ./count before running body, so a
// test can assert how many times the tool actually ran.
const countingScript = `n=$(cat count 2>/dev/null || echo 0); n=$((n+1)); echo $n > count; `

// hookContextAt is the state setup.Plugin leaves behind once it has moved the
// run into cwd under the given setup.
func hookContextAt(cwd string, setup *shell.Setup) *agent.HookContext {
	return &agent.HookContext{
		Context:  context.Background(),
		Request:  &ai.Request{Setup: setup},
		Response: &ai.Response{Workspace: &api.Workspace{Cwd: cwd}},
	}
}

func call(definition api.ToolDefinition, input map[string]any) (any, error) {
	return definition.Handler(context.Background(), input)
}

// bindAndPrepare binds the tools to a workspace over setup and runs its
// PreRun, the way promptrun's hook list does, closing it after the spec.
func bindAndPrepare(cwd string, opts exectool.WorkspaceOptions, tools api.InlineTools) (*exectool.Workspace, []api.ToolDefinition) {
	opts.Cwd = cwd
	workspace := exectool.NewWorkspace(opts)
	definitions, err := exectool.Definitions(tools, workspace)
	Expect(err).NotTo(HaveOccurred())
	Expect(workspace.PreRun(hookContextAt(cwd, opts.Setup))).To(Succeed())
	DeferCleanup(workspace.Close)
	return workspace, definitions
}

var _ = Describe("Inline exec tools", func() {
	var dir string

	BeforeEach(func() {
		var err error
		dir, err = filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
	})

	build := func(tool api.InlineTool, wrap ...shell.WrapFunc) api.ToolDefinition {
		opts := exectool.WorkspaceOptions{}
		if len(wrap) > 0 {
			opts.SandboxWrap = wrap[0]
		}
		_, definitions := bindAndPrepare(dir, opts, api.InlineTools{"probe": tool})
		Expect(definitions).To(HaveLen(1))
		return definitions[0]
	}

	attempts := func() string {
		count, err := os.ReadFile(filepath.Join(dir, "count"))
		Expect(err).NotTo(HaveOccurred())
		return strings.TrimSpace(string(count))
	}

	It("passes the input as JSON on stdin and returns stdout as text", func() {
		result, err := call(build(api.InlineTool{Exec: "cat"}), map[string]any{"id": "T-1"})

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(`{"id":"T-1"}`))
	})

	It("runs the script under its shebang interpreter", func() {
		result, err := call(build(api.InlineTool{Exec: "#!/bin/sh\necho $0"}), nil)

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal("/bin/sh"))
	})

	It("runs in the workspace cwd by default", func() {
		result, err := call(build(api.InlineTool{Exec: "pwd -P"}), nil)

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(dir))
	})

	It("converts a Picoschema input into the object schema the model sees", func() {
		definition := build(api.InlineTool{Exec: "true", Input: map[string]any{"id": "string", "verbose?": "boolean"}})

		Expect(definition.InputSchema).To(HaveKeyWithValue("type", "object"))
		Expect(definition.InputSchema).To(HaveKeyWithValue("required", ConsistOf("id")))
		Expect(definition.InputSchema["properties"]).To(HaveKey("verbose"))
	})

	Context("with an output schema", func() {
		output := map[string]any{"title": "string"}

		It("returns the parsed document when stdout satisfies it", func() {
			result, err := call(build(api.InlineTool{Exec: `echo '{"title":"done"}'`, Output: output}), nil)

			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal(map[string]any{"title": "done"}))
		})

		It("fails when stdout violates it", func() {
			_, err := call(build(api.InlineTool{Exec: `echo '{"title":7}'`, Output: output}), nil)

			Expect(err).To(MatchError(ContainSubstring("output does not match the declared schema")))
		})

		It("refuses a schema that does not compile before any call", func() {
			_, err := exectool.Definitions(api.InlineTools{"probe": {Exec: "true", Output: map[string]any{"type": 7}}},
				exectool.NewWorkspace(exectool.WorkspaceOptions{Cwd: dir}))

			Expect(err).To(MatchError(ContainSubstring("output schema")))
		})
	})

	It("reports a non-zero exit with its code and stderr", func() {
		_, err := call(build(api.InlineTool{Exec: "echo broken >&2; exit 3"}), nil)

		Expect(err).To(MatchError(And(ContainSubstring("exited with code 3"), ContainSubstring("broken"))))
	})

	It("kills an attempt that outlives its timeout", func() {
		started := time.Now()
		_, err := call(build(api.InlineTool{Exec: "sleep 30", Timeout: "200ms"}), nil)

		Expect(err).To(MatchError(ContainSubstring("timed out after 200ms")))
		Expect(time.Since(started)).To(BeNumerically("<", 5*time.Second))
	})

	It("layers the tool's env vars over the run's dotenv", func() {
		Expect(os.WriteFile(filepath.Join(dir, ".env"), []byte("FROM_RUN=run\nSHARED=run\n"), 0o600)).To(Succeed())
		_, definitions := bindAndPrepare(dir, exectool.WorkspaceOptions{Setup: &shell.Setup{DotEnv: []string{".env"}}}, api.InlineTools{"probe": {
			Exec:  `[ "$FROM_RUN" = run ] && [ "$SHARED" = tool ] && echo ok`,
			Setup: shell.Setup{EnvVars: []types.EnvVar{{Name: "SHARED", ValueStatic: "tool"}}},
		}})

		result, err := call(definitions[0], nil)

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal("ok"))
	})

	It("prepares in the workspace setup left, not where the run started", func() {
		workspace := exectool.NewWorkspace(exectool.WorkspaceOptions{Cwd: GinkgoT().TempDir()})
		definitions, err := exectool.Definitions(api.InlineTools{"probe": {Exec: "pwd -P"}}, workspace)
		Expect(err).NotTo(HaveOccurred())

		Expect(workspace.PreRun(hookContextAt(dir, nil))).To(Succeed())
		DeferCleanup(workspace.Close)
		result, err := call(definitions[0], nil)

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(dir))
	})

	It("runs a placement: sandbox tool through the run's sandbox wrapper", func() {
		wrap := func(_ context.Context, cmd string, args, env []string) (string, []string, []string, error) {
			return "env", append([]string{"CONFINED=yes", cmd}, args...), env, nil
		}

		result, err := call(build(api.InlineTool{Exec: "echo $CONFINED", Placement: api.ToolPlacementSandbox, Credentials: api.ToolCredentialsNone}, wrap), nil)

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal("yes"))
	})

	It("never wraps a host tool, even when the run has a sandbox wrapper", func() {
		wrap := func(_ context.Context, cmd string, args, env []string) (string, []string, []string, error) {
			return "env", append([]string{"CONFINED=yes", cmd}, args...), env, nil
		}

		result, err := call(build(api.InlineTool{Exec: "echo \"[$CONFINED]\""}, wrap), nil)

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal("[]"))
	})

	Context("retry", func() {
		retry := func(attempts int, exitCodes ...int) *api.ToolRetry {
			return &api.ToolRetry{Attempts: attempts, Delay: "1ms", MaxDelay: "5ms", ExitCodes: exitCodes}
		}

		It("re-runs a failing attempt until it succeeds", func() {
			result, err := call(build(api.InlineTool{
				Exec: countingScript + `[ $n -ge 3 ] || exit 75; echo ok`, Retry: retry(3),
			}), nil)

			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal("ok"))
			Expect(attempts()).To(Equal("3"))
		})

		It("reports the attempts made once they are exhausted", func() {
			_, err := call(build(api.InlineTool{Exec: countingScript + "exit 75", Retry: retry(2)}), nil)

			Expect(err).To(MatchError(ContainSubstring("failed after 2 attempts")))
			Expect(attempts()).To(Equal("2"))
		})

		It("retries only the listed exit codes", func() {
			_, err := call(build(api.InlineTool{Exec: countingScript + "exit 1", Retry: retry(3, 75)}), nil)

			Expect(err).To(MatchError(ContainSubstring("exited with code 1")))
			Expect(attempts()).To(Equal("1"))
		})

		It("retries an attempt that timed out", func() {
			result, err := call(build(api.InlineTool{
				Exec: countingScript + `[ $n -ge 2 ] || sleep 30; echo ok`, Timeout: "300ms", Retry: retry(2),
			}), nil)

			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal("ok"))
		})

		It("does not retry an output that violates the schema", func() {
			_, err := call(build(api.InlineTool{
				Exec: countingScript + `echo '{"n":"x"}'`, Output: map[string]any{"n": "integer"}, Retry: retry(3),
			}), nil)

			Expect(err).To(MatchError(ContainSubstring("output does not match")))
			Expect(attempts()).To(Equal("1"))
		})
	})

	DescribeTable("derives the default permission from readonly/destructive",
		func(tool api.InlineTool, want api.ToolPolicy) {
			tool.Exec = "true"
			resolved, err := tools.ResolveDefinitions([]api.ToolDefinition{build(tool)}, tools.ResolveOptions{})

			Expect(err).NotTo(HaveOccurred())
			Expect(resolved).To(HaveLen(1))
			Expect(resolved[0].DefaultPermission).To(Equal(want))
		},
		Entry("readonly runs without asking", api.InlineTool{ReadOnly: true}, api.ToolPolicyAllow),
		Entry("destructive asks", api.InlineTool{Destructive: true}, api.ToolPolicyAsk),
		Entry("an explicit permission wins over the hints", api.InlineTool{ReadOnly: true, Permission: api.ToolPolicyAsk}, api.ToolPolicyAsk),
	)
})
