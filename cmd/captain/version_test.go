package main

import (
	"bytes"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/cobra"
)

func TestCaptain(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Captain CLI Suite")
}

var _ = Describe("version information", func() {
	const (
		cleanVersion = "captain version v1.2.3 (commit: abc1234, built: 2026-07-27T12:34:56Z, clean)"
		dirtyVersion = "captain version v1.2.3-dirty (commit: abc1234, built: 2026-07-27T12:34:56Z, dirty)"
	)

	It("formats clean, dirty, and unstamped builds", func() {
		clean := buildInfo{Version: "v1.2.3", Commit: "abc1234", Date: "2026-07-27T12:34:56Z", Dirty: "false"}
		dirty := buildInfo{Version: "v1.2.3", Commit: "abc1234", Date: "2026-07-27T12:34:56Z", Dirty: "true"}
		unstamped := buildInfo{Version: "dev", Commit: "unknown", Date: "unknown", Dirty: "unknown"}

		Expect(clean.String()).To(Equal(cleanVersion))
		Expect(dirty.String()).To(Equal(dirtyVersion))
		Expect(unstamped.String()).To(Equal("captain version dev (commit: unknown, built: unknown, unknown)"))
	})

	It("rejects an invalid Git state", func() {
		info := buildInfo{Version: "v1.2.3", Commit: "abc1234", Date: "2026-07-27T12:34:56Z", Dirty: "maybe"}

		Expect(func() {
			info.String()
		}).To(PanicWith(`invalid build dirty state "maybe"`))
	})

	It("prints identical details for version and --version", func() {
		info := buildInfo{Version: "v1.2.3", Commit: "abc1234", Date: "2026-07-27T12:34:56Z", Dirty: "false"}

		for _, args := range [][]string{{"version"}, {"--version"}} {
			root := &cobra.Command{Use: "captain"}
			var stdout bytes.Buffer
			root.SetOut(&stdout)
			root.SetArgs(args)
			configureVersion(root, info)

			Expect(root.Execute()).To(Succeed())
			Expect(stdout.String()).To(Equal(cleanVersion + "\n"))
		}
	})
})
