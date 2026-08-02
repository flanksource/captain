package bash

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestShellTransform(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Shell Transform Suite")
}

var _ = Describe("TransformShellCommand", func() {
	It("unwraps a login zsh command without losing shell metadata", func() {
		transformed, ok := TransformShellCommand(`/bin/zsh -lc 'gavel pr status 50 --logs'`)

		Expect(ok).To(BeTrue())
		Expect(transformed).To(Equal(ShellCommand{
			Command: "gavel pr status 50 --logs",
			Shell:   "zsh",
			Flags:   []string{"-l"},
		}))
	})

	It("retains positional arguments used by the command body", func() {
		transformed, ok := TransformShellCommand(`/bin/bash -c 'printf "%s" "$1"' command-name value`)

		Expect(ok).To(BeTrue())
		Expect(transformed.Command).To(Equal(`printf "%s" "$1"`))
		Expect(transformed.Shell).To(Equal("bash"))
		Expect(transformed.Args).To(Equal([]string{"command-name", "value"}))
	})

	It("does not transform a dynamically expanded wrapper", func() {
		_, ok := TransformShellCommand(`/bin/zsh -lc "echo $HOME"`)
		Expect(ok).To(BeFalse())
	})

	It("normalizes Bash input idempotently", func() {
		input := map[string]any{"command": `/bin/zsh -lc 'pnpm test'`, "timeout": float64(1000)}

		first := TransformBashInput(input)
		second := TransformBashInput(first)

		Expect(first).To(Equal(map[string]any{
			"command":    "pnpm test",
			"shell":      "zsh",
			"shellFlags": []string{"-l"},
			"timeout":    float64(1000),
		}))
		Expect(second).To(Equal(first))
		Expect(input["command"]).To(Equal(`/bin/zsh -lc 'pnpm test'`))
	})
})
