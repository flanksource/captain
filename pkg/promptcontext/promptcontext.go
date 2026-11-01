// Package promptcontext enriches a run's initial prompt with `prompt.context`
// hooks: .prompt files whose `hook:` frontmatter matches the prompt with an RE2
// pattern and/or a CEL predicate, and whose rendered output is appended,
// prepended or substituted into Prompt.User inside a marked block.
//
// A host discovers hooks from its prompt sources (Discover, DefaultSources) and
// promptrun applies them (Apply) before a run is admitted, so the recorded spec
// and every verify retry carry the enriched prompt.
package promptcontext

import (
	"context"
	"os"
	"regexp"

	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/plans"
	"github.com/google/cel-go/cel"
)

// Event is the hook event a run's initial prompt fires.
const Event = "prompt.context"

// MetadataKey is the Prompt.Metadata key Apply records the applied hook names
// under, comma-joined in the order they ran.
const MetadataKey = "captain.context"

// blocks marks context hook output; Apply strips them before matching, so a
// re-apply (resume, preview then run) starts from the authored prompt.
var blocks = promptlib.NewHookBlocks("captain:context")

// Source is one place prompt.context hook prompts are loaded from.
type Source = promptlib.HookSource

// Hook is one discovered prompt.context hook, compiled and ready to apply.
type Hook struct {
	// Name is the prompt file's base name; it marks the hook's block.
	Name string
	// Source is where the hook was loaded from: "<label>:<file>".
	Source string
	Mode   promptlib.HookMode

	match  *regexp.Regexp
	when   cel.Program
	expr   string
	render func(ctx context.Context, data map[string]any) (string, error)
}

// AppliedHook names one hook Apply folded into the prompt.
type AppliedHook struct {
	Name   string
	Source string
}

// Applied is the hooks Apply folded into the prompt, in the order they ran.
type Applied []AppliedHook

// Names is the applied hook names in order.
func (a Applied) Names() []string {
	out := make([]string, 0, len(a))
	for _, hook := range a {
		out = append(out, hook.Name)
	}
	return out
}

// DefaultSources is the runtime search path for context hooks, the same one
// plan hooks use: the configured captain prompt dirs, then
// <workDir>/.captain/prompts when it exists. Later sources shadow earlier ones
// by file name.
func DefaultSources(cfg captainconfig.Config, workDir string) ([]Source, error) {
	dirs, err := plans.HookDirs(cfg, workDir)
	if err != nil {
		return nil, err
	}
	sources := make([]Source, 0, len(dirs))
	for _, dir := range dirs {
		sources = append(sources, Source{FS: os.DirFS(dir), Label: dir})
	}
	return sources, nil
}
