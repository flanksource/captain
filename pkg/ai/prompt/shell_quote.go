package prompt

import (
	"strings"

	"github.com/mbleigh/raymond"
)

// init registers the template helpers every .prompt render shares. raymond
// helpers are process-global (frontmatter and the dotprompt body both resolve
// them), and RegisterHelper panics on a second registration, so this is the one
// place they are installed.
func init() {
	raymond.RegisterHelper("shq", func(value any) raymond.SafeString {
		return raymond.SafeString(ShellQuote(raymond.Str(value)))
	})
}

// ShellQuote quotes s as a single POSIX shell word: wrapped in single quotes,
// with each embedded single quote closed, backslash-escaped and reopened.
// Frontmatter renders run variables into
// shell commands (workflow.verify.commands, workflow.after[].commands), so a
// caller-supplied value must go through `{{shq value}}` to stay one argument.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
