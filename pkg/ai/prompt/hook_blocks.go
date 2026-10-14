package prompt

import (
	"regexp"
	"strconv"
	"strings"
)

// HookNameRe is the shape a hook name (its file's base name) must have to mark
// a block.
var HookNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// HookBlocks marks hook output inside the content it hooks, so the output can be
// found again and stripped to recover what was authored. A block is
//
//	<!-- PREFIX:NAME -->
//	output
//	<!-- /PREFIX:NAME -->
//
// and a substituting block also carries the span it replaced, as a Go-quoted
// `replaced` attribute on its opening marker, so stripping restores that span.
type HookBlocks struct {
	prefix string
	re     *regexp.Regexp
}

// NewHookBlocks returns the block helpers for one marker prefix, e.g.
// "captain:hook" for plan hooks.
func NewHookBlocks(prefix string) HookBlocks {
	quoted := regexp.QuoteMeta(prefix)
	return HookBlocks{prefix: prefix, re: regexp.MustCompile(`(?s)<!-- ` + quoted +
		`:([a-z0-9-]+)(?: replaced=("(?:[^"\\]|\\.)*"))? -->\n?(.*?)\n?<!-- /` + quoted + `:([a-z0-9-]+) -->`)}
}

// Wrap marks output as the named hook's block.
func (b HookBlocks) Wrap(name, output string) string {
	return b.open(name, "") + "\n" + output + "\n" + b.close(name)
}

// WrapReplacing marks output as the named hook's block standing in for replaced.
func (b HookBlocks) WrapReplacing(name, output, replaced string) string {
	return b.open(name, " replaced="+strconv.Quote(replaced)) + "\n" + output + "\n" + b.close(name)
}

func (b HookBlocks) open(name, attrs string) string {
	return "<!-- " + b.prefix + ":" + name + attrs + " -->"
}

func (b HookBlocks) close(name string) string {
	return "<!-- /" + b.prefix + ":" + name + " -->"
}

// Find returns the text of every block in content, keyed by hook name.
func (b HookBlocks) Find(content string) map[string]string {
	blocks := map[string]string{}
	for _, match := range b.re.FindAllStringSubmatch(content, -1) {
		if match[1] == match[4] {
			blocks[match[1]] = strings.TrimSpace(match[3])
		}
	}
	return blocks
}

// Strip returns content without its blocks — a substituting block gives back
// the span it replaced — with line endings normalized and outer space trimmed.
func (b HookBlocks) Strip(content string) string {
	content = strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
	stripped := b.re.ReplaceAllStringFunc(content, func(match string) string {
		parts := b.re.FindStringSubmatch(match)
		if parts[1] != parts[4] {
			return match
		}
		if parts[2] == "" {
			return ""
		}
		replaced, err := strconv.Unquote(parts[2])
		if err != nil {
			return match
		}
		return replaced
	})
	return strings.TrimSpace(stripped)
}
