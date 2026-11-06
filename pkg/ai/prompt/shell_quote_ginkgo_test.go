package prompt

import (
	"os/exec"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("shq helper", func() {
	DescribeTable("quotes a value as one POSIX shell word",
		func(value, quoted string) {
			Expect(ShellQuote(value)).To(Equal(quoted))
			// The property that matters: the shell reads back exactly the value.
			out, err := exec.Command("sh", "-c", "printf %s "+quoted).Output()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(out)).To(Equal(value))
		},
		Entry("plain", "topic", `'topic'`),
		Entry("spaces", "fix the  bug", `'fix the  bug'`),
		Entry("single quotes", "it's 'done'", `'it'\''s '\''done'\'''`),
		Entry("command substitution", "$(rm -rf /)", `'$(rm -rf /)'`),
		Entry("empty", "", `''`),
	)

	It("renders in frontmatter and body, beside plain run variables", func() {
		source := `---
setup:
  cwd: "{{worktree}}"
workflow:
  verify:
    commands: ["go test ./..."]
  after:
    - cwd: "{{repo}}"
      commands:
        - git -C {{repo}} merge --ff-only {{shq branch}}
---
Merge {{shq branch}} into {{repo}}.
`
		data := map[string]any{"repo": "/src/repo", "worktree": "/src/wt", "branch": "topic's $(rm -rf /)"}
		req, _, err := Load(source).Render(RenderOptions{Data: data, Declared: true})
		Expect(err).NotTo(HaveOccurred())

		Expect(req.Setup.Cwd).To(Equal("/src/wt"))
		Expect(req.Workflow.After).To(Equal([]api.AfterStep{{
			Cwd:        "/src/repo",
			HookAction: api.HookAction{Commands: []string{`git -C /src/repo merge --ff-only 'topic'\''s $(rm -rf /)'`}},
		}}))
		Expect(req.Prompt.User).To(Equal(`Merge 'topic'\''s $(rm -rf /)' into /src/repo.`))
	})
})

var _ = Describe("join helper", func() {
	render := func(source string, files any) (string, error) {
		req, _, err := Load(source).Render(RenderOptions{Data: map[string]any{"files": files}, Declared: true})
		return req.Prompt.User, err
	}

	DescribeTable("joins a list variable with the separator",
		func(files any, rendered string) {
			got, err := render(`Review {{join files ", "}}.`, files)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(rendered))
		},
		Entry("typed --var list", []any{"pkg/a.go", "cmd/main.go"}, "Review pkg/a.go, cmd/main.go."),
		Entry("lines list", []string{"pkg/a.go", "cmd/main.go"}, "Review pkg/a.go, cmd/main.go."),
		Entry("one item", []string{"pkg/a.go"}, "Review pkg/a.go."),
		Entry("empty", []string{}, "Review ."),
		Entry("missing, like any unset {{var}}", nil, "Review ."),
	)

	It("fails the render when the value is not a list", func() {
		_, err := render(`Review {{join files ", "}}.`, "pkg/a.go")
		Expect(err).To(MatchError(ContainSubstring("join: got a string, want a list")))
	})
})
