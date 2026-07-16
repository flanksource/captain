package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

type CommandAliasOptions struct {
	Name   string
	Short  string
	Hidden bool
	Root   *cobra.Command
	Target []string
}

func NewCommandAlias(opts CommandAliasOptions) *cobra.Command {
	if opts.Name == "" {
		panic("command alias name is required")
	}
	if opts.Root == nil {
		panic(fmt.Sprintf("command alias %s root is required", opts.Name))
	}
	if len(opts.Target) == 0 {
		panic(fmt.Sprintf("command alias %s target is required", opts.Name))
	}
	return &cobra.Command{
		Use:                opts.Name,
		Short:              opts.Short,
		Hidden:             opts.Hidden,
		DisableFlagParsing: true,
		RunE: func(_ *cobra.Command, args []string) error {
			forwarded := make([]string, 0, len(opts.Target)+len(args))
			forwarded = append(forwarded, opts.Target...)
			forwarded = append(forwarded, args...)
			opts.Root.SetArgs(forwarded)
			silenceErrors := opts.Root.SilenceErrors
			opts.Root.SilenceErrors = true
			defer func() { opts.Root.SilenceErrors = silenceErrors }()
			return opts.Root.Execute()
		},
	}
}
