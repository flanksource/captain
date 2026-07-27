package cli

import (
	"bytes"
	"errors"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"
)

var _ = Describe("command aliases", func() {
	It("redispatches raw arguments through the canonical command", func() {
		root := &cobra.Command{Use: "captain", SilenceErrors: true, SilenceUsage: true}
		prompt := &cobra.Command{Use: "prompt"}
		var received []string
		prompt.AddCommand(&cobra.Command{
			Use:                "run",
			DisableFlagParsing: true,
			RunE: func(_ *cobra.Command, args []string) error {
				received = append([]string(nil), args...)
				return nil
			},
		})
		root.AddCommand(prompt)
		ai := &cobra.Command{Use: "ai"}
		ai.AddCommand(NewCommandAlias(CommandAliasOptions{
			Name: "prompt", Root: root, Target: []string{"prompt", "run"},
		}))
		root.AddCommand(ai)

		args := []string{"--attach", "diagram.png", "-p", "describe it"}
		root.SetArgs(append([]string{"ai", "prompt"}, args...))
		Expect(root.Execute()).To(Succeed())
		Expect(received).To(Equal(args))
	})

	It("prints a canonical command error once", func() {
		root := &cobra.Command{Use: "captain", SilenceUsage: true}
		var output bytes.Buffer
		root.SetErr(&output)
		prompt := &cobra.Command{Use: "prompt"}
		prompt.AddCommand(&cobra.Command{
			Use: "run",
			RunE: func(_ *cobra.Command, _ []string) error {
				return errors.New("canonical failure")
			},
		})
		root.AddCommand(prompt)
		ai := &cobra.Command{Use: "ai"}
		ai.AddCommand(NewCommandAlias(CommandAliasOptions{
			Name: "prompt", Root: root, Target: []string{"prompt", "run"},
		}))
		root.AddCommand(ai)
		root.SetArgs([]string{"ai", "prompt"})

		Expect(root.Execute()).To(MatchError("canonical failure"))
		Expect(strings.Count(output.String(), "Error: canonical failure")).To(Equal(1))
	})
})
