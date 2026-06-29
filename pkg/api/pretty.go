package api

import (
	"fmt"

	clickyapi "github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
)

// orDash renders an empty string as a dash so blank fields stay visible.
func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// Pretty renders a one-line cost summary suitable for a table cell or status line.
func (c Cost) Pretty() clickyapi.Text {
	return clickyapi.Text{}.
		Appendf("$%.4f", c.Total()).Space().
		Appendf("· %d in / %d out", c.InputTokens, c.OutputTokens)
}

// summary renders the git pin as "repo@sha (PR pr)".
func (g Git) summary() string {
	s := orDash(g.Repo)
	if g.SHA != "" {
		s += "@" + g.SHA
	}
	if g.PR != "" {
		s += " (PR " + g.PR + ")"
	}
	return s
}

// Pretty renders a one-line permissions summary instead of a noisy field dump.
func (p Permissions) Pretty() clickyapi.Text {
	mode := string(p.Mode)
	if mode == "" {
		mode = "default"
	}
	t := clickyapi.Text{}.Append("mode=").Append(mode, "font-medium")
	if n := len(p.Tools.Allow); n > 0 {
		t = t.Appendf(" · %d allow", n)
	}
	if n := len(p.Tools.Deny); n > 0 {
		t = t.Appendf(" · %d deny", n)
	}
	if p.MCP.Disabled {
		t = t.Append(" · mcp:off")
	}
	if len(p.Presets) > 0 {
		t = t.Appendf(" · presets=%v", p.Presets)
	}
	return t
}

// Pretty renders the workspace as a small tree: the working dir, then git, file
// count, and worktree branch as indented children.
func (c Context) Pretty() clickyapi.Text {
	t := clickyapi.Text{}.Add(icons.Folder).Space().Append(orDash(c.Dir), "font-medium")
	if c.Git != nil {
		t = t.NewLine().Append("  ").Add(icons.Git).Space().Append(c.Git.summary())
	}
	if n := len(c.Files); n > 0 {
		t = t.NewLine().Append("  ").Add(icons.File).Space().Appendf("%d changed file(s)", n)
	}
	if c.Worktree != nil && c.Worktree.Branch != "" {
		t = t.NewLine().Append("  ").Add(icons.Git).Space().Appendf("worktree %s", c.Worktree.Branch)
	}
	return t
}

// Pretty renders the spec as a compact multi-line summary headed by the model.
func (s Spec) Pretty() clickyapi.Text {
	t := clickyapi.Text{}.Add(icons.Robot).Space().
		Append("Spec", "font-bold text-blue-600").NewLine().
		Append("  model: ").Append(orDash(s.Name), "font-medium")
	if s.Effort != "" {
		t = t.Appendf(" (effort=%s)", s.Effort)
	}
	if s.Budget.Cost > 0 || s.Budget.MaxTokens > 0 {
		t = t.NewLine().Append(fmt.Sprintf("  budget: $%.2f / %d tokens", s.Budget.Cost, s.Budget.MaxTokens))
	}
	t = t.NewLine().Append("  perms: ").Add(s.Permissions.Pretty())
	if s.Context.Dir != "" || s.Context.Git != nil {
		t = t.NewLine().Append("  ").Add(s.Context.Pretty())
	}
	return t
}
