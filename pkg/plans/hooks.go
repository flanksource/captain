package plans

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/ai/prompt/hookrun"
	"github.com/flanksource/captain/pkg/captainconfig"
)

// RevisionEvent is the hook event a plan revision save fires.
const RevisionEvent = "plan.revision"

//go:embed hooks/*.prompt
var defaultHooks embed.FS

// HookInput is what a hook sees: the plan identity and the revision body with
// every hook block stripped, so a hook never summarizes its own earlier output.
type HookInput struct {
	Title    string
	Markdown string
	// Prior is the stripped body of the plan's latest revision, "" for none.
	Prior string
}

// Hook rewrites or decorates a plan revision before it is persisted. Hooks are
// .prompt files discovered at runtime (DiscoverHooks); Run is exposed so tests
// and hosts can supply one without a model.
type Hook struct {
	// Name is the prompt file's base name; it marks the hook's block.
	Name string
	Mode promptlib.HookMode
	// Source is where the hook was loaded from, for error messages.
	Source string
	Run    func(ctx context.Context, input HookInput) (string, error)
}

// HookDirs is the runtime search path for hook prompts after the embedded
// defaults: the configured captain prompt dirs, then <workDir>/.captain/prompts
// when it exists. Later directories shadow earlier ones by file name.
func HookDirs(cfg captainconfig.Config, workDir string) ([]string, error) {
	var dirs []string
	if len(cfg.Prompts.Dirs) > 0 {
		configPath, err := captainconfig.Path()
		if err != nil {
			return nil, err
		}
		for _, raw := range cfg.Prompts.Dirs {
			dir, err := promptlib.ResolveDir(raw, filepath.Dir(configPath))
			if err != nil {
				return nil, err
			}
			dirs = append(dirs, dir)
		}
	}
	if workDir != "" {
		local := filepath.Join(workDir, ".captain", "prompts")
		if info, err := os.Stat(local); err == nil && info.IsDir() {
			dirs = append(dirs, local)
		} else if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat %s: %w", local, err)
		}
	}
	return dirs, nil
}

// DiscoverHooks loads every plan.revision hook prompt from captain's embedded
// defaults and then dirs, in order. A same-named prompt in a later source
// replaces an earlier one; `hook: {disabled: true}` removes it. Hooks run in
// name order.
func DiscoverHooks(dirs ...string) ([]Hook, error) {
	byName := map[string]Hook{}
	sources := []fs.FS{defaultHooks}
	labels := []string{"embedded"}
	for _, dir := range dirs {
		sources = append(sources, os.DirFS(dir))
		labels = append(labels, dir)
	}
	for i, fsys := range sources {
		if err := loadHooks(fsys, labels[i], byName); err != nil {
			return nil, err
		}
	}
	hooks := make([]Hook, 0, len(byName))
	for _, hook := range byName {
		hooks = append(hooks, hook)
	}
	sort.Slice(hooks, func(i, j int) bool { return hooks[i].Name < hooks[j].Name })
	return hooks, nil
}

func loadHooks(fsys fs.FS, label string, byName map[string]Hook) error {
	return fs.WalkDir(fsys, ".", func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || path.Ext(file) != ".prompt" {
			return nil
		}
		tmpl, err := promptlib.LoadFS(fsys, file)
		if err != nil {
			return err
		}
		source := label + ":" + file
		spec, err := tmpl.Hook()
		if err != nil {
			return fmt.Errorf("%s: %w", source, err)
		}
		if spec == nil {
			return nil
		}
		name := strings.TrimSuffix(path.Base(file), ".prompt")
		if !promptlib.HookNameRe.MatchString(name) {
			return fmt.Errorf("%s: hook name %q must match %s", source, name, promptlib.HookNameRe)
		}
		if spec.Disabled {
			delete(byName, name)
			return nil
		}
		if spec.On != RevisionEvent {
			return nil
		}
		if err := validateRevisionHook(*spec); err != nil {
			return fmt.Errorf("%s: %w", source, err)
		}
		byName[name] = Hook{Name: name, Mode: spec.Mode, Source: source, Run: templateRun(tmpl, name, spec.Field)}
		return nil
	})
}

// validateRevisionHook rejects the hook keys plan.revision has no meaning for:
// a revision hook always runs, on the whole body, through its model.
func validateRevisionHook(spec promptlib.HookSpec) error {
	if spec.Mode == promptlib.HookSubstitute {
		return fmt.Errorf("%s hooks do not support mode %s", RevisionEvent, spec.Mode)
	}
	switch {
	case spec.Match != "":
		return fmt.Errorf("%s hooks do not support match", RevisionEvent)
	case spec.When != "":
		return fmt.Errorf("%s hooks do not support when", RevisionEvent)
	case spec.Render != "":
		return fmt.Errorf("%s hooks do not support render", RevisionEvent)
	}
	return nil
}

// templateRun executes a hook prompt with the model its frontmatter declares.
func templateRun(tmpl *promptlib.Template, name, field string) func(context.Context, HookInput) (string, error) {
	return func(ctx context.Context, input HookInput) (string, error) {
		return hookrun.Execute(ctx, tmpl, hookrun.Options{Name: "plan hook " + name, Field: field, Data: map[string]any{
			"title": input.Title, "plan": input.Markdown, "prior": input.Prior,
		}})
	}
}

// applyHooks runs each hook over the stripped body. When the body equals the
// prior revision's, the prior revision (with its blocks) is reused and no hook
// runs, so re-saving an unchanged plan costs nothing.
func applyHooks(ctx context.Context, hooks []Hook, title, markdown, prior string) (string, error) {
	body := StripBlocks(markdown)
	priorBody := StripBlocks(prior)
	if len(hooks) == 0 {
		return markdown, nil
	}
	if prior != "" && body == priorBody {
		return prior, nil
	}
	var before, after []string
	for _, hook := range hooks {
		output, err := hook.Run(ctx, HookInput{Title: title, Markdown: body, Prior: priorBody})
		if err != nil {
			return "", fmt.Errorf("plan hook %s (%s): %w", hook.Name, hook.Source, err)
		}
		output = strings.TrimSpace(output)
		if output == "" {
			return "", fmt.Errorf("plan hook %s (%s) returned empty output", hook.Name, hook.Source)
		}
		switch hook.Mode {
		case promptlib.HookReplace:
			body = output
		case promptlib.HookPrepend:
			before = append(before, block(hook.Name, output))
		case promptlib.HookAppend:
			after = append(after, block(hook.Name, output))
		default:
			return "", fmt.Errorf("plan hook %s (%s): unsupported mode %q", hook.Name, hook.Source, hook.Mode)
		}
	}
	return strings.Join(append(append(before, body), after...), "\n\n"), nil
}

var hookBlocks = promptlib.NewHookBlocks("captain:hook")

func block(name, output string) string { return hookBlocks.Wrap(name, output) }

// Blocks returns the text of every hook block in markdown, keyed by hook name.
func Blocks(markdown string) map[string]string { return hookBlocks.Find(markdown) }

// StripBlocks returns markdown without its hook blocks: the plan body a person
// or agent authored.
func StripBlocks(markdown string) string { return hookBlocks.Strip(markdown) }
