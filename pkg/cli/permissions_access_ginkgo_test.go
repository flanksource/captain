package cli

import (
	"context"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/runtimeprofiles"
)

const legacyReviewPrompt = `---
description: A read-only review.
model: claude-sonnet-5
mode: agent
permissions:
  tools:
    Read: allow
    edit: deny
    write: deny
    shell: deny
memory:
  skipUser: true
---
Review {{files}}.
`

var _ = Describe("--access", func() {
	DescribeTable("lands on the request permissions",
		func(flag string, want api.AccessLevel, wantErr string) {
			spec, err := AIRuntimeOptions{Access: flag}.requestSpec()
			if wantErr != "" {
				Expect(err).To(MatchError(ContainSubstring(wantErr)))
				return
			}
			Expect(err).NotTo(HaveOccurred())
			Expect(spec.Permissions.Access).To(Equal(want))
		},
		Entry("read-only", "read-only", api.AccessReadOnly, ""),
		Entry("unset", "", api.AccessLevel(""), ""),
		Entry("unknown", "readonly", api.AccessLevel(""), `invalid --access "readonly"`),
	)
})

var _ = Describe("captain permissions explain", func() {
	It("shows the compiled posture of a review prompt on claude and codex", func() {
		captainconfig.SetPathForTesting(filepath.Join(GinkgoT().TempDir(), "captain.yaml"))
		DeferCleanup(func() { captainconfig.SetPathForTesting("") })
		catalog, err := runtimeprofiles.NewCatalog(runtimeprofiles.NewBuiltinSource())
		Expect(err).NotTo(HaveOccurred())
		ctx := ContextWithRuntimeCatalog(context.Background(), catalog)

		out, err := RunPermissionsExplain(ctx, PermissionsExplainOptions{Prompt: "proposal-review",
			Models: []string{"agent:sonnet", "agent:gpt-6.1-sol"}, Presets: []string{"lint"}})
		Expect(err).NotTo(HaveOccurred())
		result := out.(PermissionsExplainResult)

		Expect(result.Access).To(Equal(&api.AccessState{Level: api.AccessReadOnly, Dialect: api.AccessDialectPortable,
			Source: "permissions.access", Layer: result.Access.Layer}))
		Expect(result.Runtimes).To(HaveLen(2))
		claude, codex := result.Runtimes[0], result.Runtimes[1]
		Expect(claude.Refused).To(BeEmpty())
		Expect(claude.Runtime).To(Equal("anthropic agent"))
		Expect(claude.Report.DeniedTools).To(ContainElements("Bash", "Edit", "Write", "Agent"))
		Expect(codex.Refused).To(BeEmpty())
		Expect(codex.Runtime).To(Equal("openai agent"))
		Expect(codex.Report.Sandbox).To(Equal("read-only"))
		Expect(codex.Report.Approval).To(Equal("never"))
		Expect(result.Suggestion.Reason).To(Equal("permissions.access is already read-only"))
	})
})

var _ = Describe("captain permissions convert", func() {
	write := func(name, body string) string {
		path := filepath.Join(GinkgoT().TempDir(), name)
		Expect(os.WriteFile(path, []byte(body), 0o644)).To(Succeed())
		return path
	}

	It("prints the portable form of a .prompt without writing it", func() {
		path := write("review.prompt", legacyReviewPrompt)

		out, err := RunPermissionsConvert(PermissionsConvertOptions{File: path})
		Expect(err).NotTo(HaveOccurred())
		result := out.(PermissionsConvertResult)

		Expect(result.Diff).To(ContainSubstring("+  access: read-only\n"))
		for _, dropped := range []string{"edit", "write", "shell"} {
			Expect(result.Diff).To(ContainSubstring("-    " + dropped + ": deny\n"))
			Expect(result.Diff).NotTo(ContainSubstring("+    " + dropped + ": deny\n"))
		}
		Expect(result.Diff).NotTo(ContainSubstring("memory"))
		Expect(result.Conversion.Residual).To(ConsistOf("permissions.tools Read: allow"))
		Expect(result.Written).To(BeFalse())
		Expect(os.ReadFile(path)).To(Equal([]byte(legacyReviewPrompt)))
	})

	It("rewrites the read-only preset in place with its web denies kept", func() {
		preset, err := os.ReadFile("../runtimeprofiles/builtin/presets/read-only.yaml")
		Expect(err).NotTo(HaveOccurred())
		path := write("read-only.yaml", string(preset))

		out, err := RunPermissionsConvert(PermissionsConvertOptions{File: path, Write: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(out.(PermissionsConvertResult).Conversion.Residual).To(ConsistOf(
			"permissions.tools web_fetch: deny", "permissions.tools web_search: deny"))

		converted, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(converted)).To(ContainSubstring("spec:\n  permissions:\n    access: read-only\n    tools:\n      web_fetch: deny\n      web_search: deny\n"))
		Expect(string(converted)).To(HavePrefix("name: Read-only\n"))
	})

	It("refuses a block that is not read-only, with the reason", func() {
		path := write("plan.yaml", "spec:\n  permissions:\n    mode: plan\n    tools:\n      read: allow\n      shell: deny\n")

		_, err := RunPermissionsConvert(PermissionsConvertOptions{File: path})
		Expect(err).To(MatchError(ContainSubstring("not converted: permissions.mode plan permits the plan-file write")))
	})
})
