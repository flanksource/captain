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
// run into cwd.
func hookContextAt(cwd string) *agent.HookContext {
	return &agent.HookContext{
		Context:  context.Background(),
		Request:  &ai.Request{},
		Response: &ai.Response{Workspace: &api.Workspace{Cwd: cwd}},
	}
}

var _ = Describe("Inline exec tools", func() {
	var dir string

	BeforeEach(func() {
		var err error
		dir, err = filepath.EvalSymlinks(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
	})

	build := func(tool api.InlineTool, opts ...func(*exectool.Workspace) shell.WrapFunc) api.ToolDefinition {
		workspace := exectool.NewWorkspace(dir, nil)
		var wrap shell.WrapFunc
		for _, opt := range opts {
			wrap = opt(workspace)
		}
		definitions, err := exectool.Definitions(api.InlineTools{"probe": tool}, workspace, wrap)
		Expect(err).NotTo(HaveOccurred())
		Expect(definitions).To(HaveLen(1))
		return definitions[0]
	}

	call := func(definition api.ToolDefinition, input map[string]any) (any, error) {
		return definition.Handler(context.Background(), input)
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
				exectool.NewWorkspace(dir, nil), nil)

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
		workspace := exectool.NewWorkspace(dir, &shell.Setup{DotEnv: []string{".env"}})
		definitions, err := exectool.Definitions(api.InlineTools{"probe": {
			Exec:  `[ "$FROM_RUN" = run ] && [ "$SHARED" = tool ] && echo ok`,
			Setup: shell.Setup{EnvVars: []types.EnvVar{{Name: "SHARED", ValueStatic: "tool"}}},
		}}, workspace, nil)
		Expect(err).NotTo(HaveOccurred())

		result, err := call(definitions[0], nil)

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal("ok"))
	})

	It("reads the workspace at call time, after setup has relocated the run", func() {
		workspace := exectool.NewWorkspace(GinkgoT().TempDir(), nil)
		definitions, err := exectool.Definitions(api.InlineTools{"probe": {Exec: "pwd -P"}}, workspace, nil)
		Expect(err).NotTo(HaveOccurred())

		Expect(workspace.PreRun(hookContextAt(dir))).To(Succeed())
		result, err := call(definitions[0], nil)

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(dir))
	})

	It("runs the command through the confinement wrapper", func() {
		wrapped := func(*exectool.Workspace) shell.WrapFunc {
			return func(_ context.Context, cmd string, args, env []string) (string, []string, []string, error) {
				return "env", append([]string{"CONFINED=yes", cmd}, args...), env, nil
			}
		}

		result, err := call(build(api.InlineTool{Exec: "echo $CONFINED"}, wrapped), nil)

		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal("yes"))
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
