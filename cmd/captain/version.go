package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
	dirty   = "unknown"
)

type buildInfo struct {
	Version string
	Commit  string
	Date    string
	Dirty   string
}

func currentBuildInfo() buildInfo {
	return buildInfo{
		Version: version,
		Commit:  commit,
		Date:    date,
		Dirty:   dirty,
	}
}

func configureVersion(root *cobra.Command, info buildInfo) {
	root.Version = info.String()
	root.SetVersionTemplate("{{.Version}}\n")
	root.AddCommand(newVersionCommand(info))
}

func newVersionCommand(info buildInfo) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), info.String())
		},
	}
}

func (info buildInfo) String() string {
	version := info.Version
	status := info.Dirty
	switch info.Dirty {
	case "true":
		version += "-dirty"
		status = "dirty"
	case "false":
		status = "clean"
	case "unknown":
	default:
		panic(fmt.Sprintf("invalid build dirty state %q", info.Dirty))
	}

	return fmt.Sprintf(
		"captain version %s (commit: %s, built: %s, %s)",
		version,
		info.Commit,
		info.Date,
		status,
	)
}
