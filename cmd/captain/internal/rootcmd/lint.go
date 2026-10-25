package rootcmd

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/flanksource/captain/pkg/cli"
	"github.com/flanksource/captain/pkg/lint/promptfile"
	"github.com/flanksource/clicky"
	"github.com/flanksource/gavel/linters"
	"github.com/flanksource/gavel/models"
	"github.com/spf13/cobra"
)

type promptfileLintOptions struct {
	Paths []string `args:"true" help:"Promptfiles or directories (default: current directory)"`
	Var   []string `flag:"var" short:"V" help:"Sample template input as key=value; repeat for multiple inputs"`
	Vars  string   `flag:"vars" clicky:"cli-file-read" help:"Sample template inputs as a JSON object, or @file"`
}

func RegisterLintCommand(root *cobra.Command, isStdinAvailable func() bool) {
	parent := &cobra.Command{Use: "lint", Short: "Validate Captain configuration files"}
	clicky.MarkLocalOnly(parent)
	root.AddCommand(parent)
	command := clicky.AddNamedCommandWithContext("promptfile", parent, promptfileLintOptions{}, func(ctx context.Context, options promptfileLintOptions) (promptfile.Results, error) {
		workDir, err := os.Getwd()
		if err != nil {
			return promptfileLintFailure(err)
		}
		var stdin []byte
		if isStdinAvailable() {
			stdin, err = io.ReadAll(os.Stdin)
			if err != nil {
				return promptfileLintFailure(err)
			}
		}
		variables, err := cli.ParsePromptVariables(cli.AIPromptOptions{Var: options.Var}, options.Vars, string(stdin), false)
		if err != nil {
			return promptfileLintFailure(err)
		}
		results := promptfile.Run(ctx, promptfile.Options{WorkDir: workDir, Paths: options.Paths, Data: variables.Data})
		if results[0].Test().Sum().Failed > 0 {
			return results, errors.New("promptfile lint failed")
		}
		return results, nil
	})
	command.Use = "promptfile [paths...]"
	command.Short = "Validate .prompt declarations and template syntax without running a model"
	command.Long = "Validate .prompt files using Gavel's linter framework. Directory discovery respects Git ignore rules. Missing frontmatter inputs warn and defer declaration checks; use --var or --vars for full validation. Missing body variables are allowed. Warnings exit successfully; errors exit nonzero."
}

func promptfileLintFailure(err error) (promptfile.Results, error) {
	return promptfile.Results{&linters.LinterResult{Linter: "promptfile", Error: err.Error(), Violations: []models.Violation{}}}, err
}
