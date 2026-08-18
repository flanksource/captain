package main

import (
	"context"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("standalone entrypoint", func() {
	It("runs the serve command when only main.go is passed to go run", func() {
		const compileTimeout = time.Minute
		ctx, cancel := context.WithTimeout(context.Background(), compileTimeout)
		defer cancel()

		cmd := exec.CommandContext(ctx, "go", "run", "./main.go", "serve", "--help")
		output, err := cmd.CombinedOutput()

		Expect(ctx.Err()).NotTo(Equal(context.DeadlineExceeded), string(output))
		Expect(err).NotTo(HaveOccurred(), string(output))
		Expect(string(output)).To(ContainSubstring("With --dev, Captain also starts the Vite dev server"))
	})
})
