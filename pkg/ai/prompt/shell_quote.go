package prompt

import (
	"fmt"
	"reflect"
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
	// {{join files ", "}}: a plain {{files}} concatenates a list's items with
	// no separator. An unset value renders empty, as {{files}} would; input
	// validation, not the template, enforces required variables.
	raymond.RegisterHelper("join", func(values any, separator string) raymond.SafeString {
		if values == nil {
			return ""
		}
		list := reflect.ValueOf(values)
		if list.Kind() != reflect.Slice && list.Kind() != reflect.Array {
			// raymond turns a helper panic into the render's error.
			panic(fmt.Errorf("join: got a %T, want a list", values))
		}
		items := make([]string, list.Len())
		for i := range items {
			items[i] = raymond.Str(list.Index(i).Interface())
		}
		return raymond.SafeString(strings.Join(items, separator))
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
