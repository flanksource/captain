package main

import (
	"bytes"

	"github.com/flanksource/captain/cmd/captain/internal/rootcmd"
	"github.com/flanksource/clicky"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"
)

var _ = Describe("root help", func() {
	// commonsMarker is a property only the commons help block documents, so its
	// presence distinguishes the appended block from cobra's own output.
	const commonsMarker = "http.har.maxBodySize"

	newRoot := func() (*cobra.Command, *cobra.Command, *bytes.Buffer) {
		out := &bytes.Buffer{}
		root := &cobra.Command{Use: "captain", Short: "test root"}
		child := &cobra.Command{Use: "child", Short: "test child", Run: func(*cobra.Command, []string) {}}
		root.AddCommand(child)
		root.SetOut(out)
		root.SetErr(out)
		rootcmd.InstallRootHelp(root)
		return root, child, out
	}

	It("appends the commons runtime knobs to the root help", func() {
		root, _, out := newRoot()
		root.SetArgs([]string{"--help"})

		Expect(root.Execute()).To(Succeed())
		Expect(out.String()).To(ContainSubstring("Usage:"), "cobra's own help must still render")
		Expect(out.String()).To(ContainSubstring("Available Commands:"))
		Expect(out.String()).To(ContainSubstring(commonsMarker))
		Expect(out.String()).To(ContainSubstring("HTTP wire logging"))
	})

	It("leaves subcommand help untouched", func() {
		root, _, out := newRoot()
		root.SetArgs([]string{"child", "--help"})

		Expect(root.Execute()).To(Succeed())
		Expect(out.String()).To(ContainSubstring("Usage:"))
		Expect(out.String()).ToNot(ContainSubstring(commonsMarker))
	})

	It("drops ANSI escapes when --no-color is set", func() {
		defer func(previous bool) { clicky.Flags.NoColor = previous }(clicky.Flags.NoColor)
		clicky.Flags.NoColor = true

		root, _, out := newRoot()
		root.SetArgs([]string{"--help"})

		Expect(root.Execute()).To(Succeed())
		Expect(out.String()).To(ContainSubstring(commonsMarker))
		Expect(out.String()).ToNot(ContainSubstring("\x1b["))
	})
})
