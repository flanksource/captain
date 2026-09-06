package rootcmd

import (
	"context"

	"github.com/flanksource/captain/pkg/cli"
	"github.com/flanksource/clicky"
	"github.com/spf13/cobra"
)

func RegisterAIRuntimeCommands(root *cobra.Command) {
	aiCmd := &cobra.Command{
		Use:   "ai",
		Short: "AI provider commands",
		Long: "AI provider commands.\n\n" +
			"Logging: increase application verbosity with -v/-vv or --log-level=debug. " +
			"HTTP calls to the provider APIs are logged on the same ladder (with credentials " +
			"redacted): failed requests are logged by default, -v adds an access line per " +
			"request, -vv adds headers and query params, -vvv request bodies, -vvvv response " +
			"bodies. Use -Plog.level.http=<level> to raise only HTTP logging, or " +
			"-Phttp.har=<path> to write the exchanges to a HAR archive instead.",
	}
	root.AddCommand(aiCmd)
	aiCmd.AddCommand(cli.NewCommandAlias(cli.CommandAliasOptions{
		Name:   "prompt",
		Short:  "Alias for captain prompt run",
		Root:   root,
		Target: []string{"prompt", "run"},
	}))
	var agentCmd *cobra.Command
	agentCmd = clicky.AddNamedCommand("agent", aiCmd, cli.AIAgentOptions{}, func(opts cli.AIAgentOptions) (any, error) {
		opts.AIRuntimeOptions = opts.WithChangedFlags(agentCmd.Flags())
		return cli.RunAIAgent(opts)
	})
	agentCmd.Short = "Run an iterative agent with verifiers, worktree, and commit"
	clicky.AddNamedCommand("models", aiCmd, cli.AIModelsOptions{}, cli.RunAIModels)
	var testCmd *cobra.Command
	testCmd = clicky.AddNamedCommand("test", aiCmd, cli.AITestOptions{}, func(opts cli.AITestOptions) (any, error) {
		opts.AIProviderOptions = (cli.AIRuntimeOptions{AIProviderOptions: opts.AIProviderOptions}).WithChangedFlags(testCmd.Flags()).AIProviderOptions
		return cli.RunAITest(opts)
	})
	clicky.AddNamedCommand("fixture", aiCmd, cli.AIFixtureOptions{}, cli.RunAIFixture).Short = "Run a YAML fixture across multiple Claude configurations"
	clicky.AddNamedCommandWithContext("mock", aiCmd, cli.AIMockOptions{}, cli.RunAIMock).Short = "Serve scripted OpenAI/Anthropic replies so agent runs spend no tokens"

	var verifyCmd *cobra.Command
	verifyCmd = clicky.AddNamedCommandWithContext("verify", root, cli.VerifyOptions{}, func(ctx context.Context, opts cli.VerifyOptions) (any, error) {
		opts.AIProviderOptions = (cli.AIRuntimeOptions{AIProviderOptions: opts.AIProviderOptions}).WithChangedFlags(verifyCmd.Flags()).AIProviderOptions
		return cli.RunVerify(ctx, opts)
	})
	verifyCmd.Short = "Run a workflow's verification checks and report the verdict"
	verifyCmd.Long = "Run the checks an api.Workflow declares — shell commands, LLM-judge prompts, and a fixture document handed to the configured fixture runner — against a working tree, and print each check's report. Exits non-zero when any check fails or cannot reach a verdict."
	clicky.MarkLocalOnly(verifyCmd)
}
