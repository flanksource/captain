package cli

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/shell"
)

// resolvePromptTemplate picks the prompt source for `captain ai prompt` and loads
// it as a dotprompt template. Precedence: positional file path > --prompt/-p value
// (literal text, or file content clicky already loaded from an @ reference) >
// piped stdin. usedStdin reports whether stdin became the prompt source, so the
// caller knows not to also expose it as the {{input}} template variable.
func resolvePromptTemplate(opts AIPromptOptions, stdin string) (tmpl *prompt.Template, usedStdin bool, err error) {
	switch {
	case opts.File != "":
		t, err := prompt.LoadFile(opts.File)
		return t, false, err
	case opts.Prompt != "":
		return prompt.Load(opts.Prompt), false, nil
	case strings.TrimSpace(stdin) != "":
		return prompt.Load(stdin), true, nil
	case len(opts.Attach) > 0:
		return prompt.Load(""), false, nil
	default:
		return nil, false, fmt.Errorf("prompt or attachment required: pass a .prompt file, --prompt/-p text, --attach/-A, or pipe via stdin")
	}
}

// parseVars turns repeated --var key=value flags into the template data map.
func parseVars(pairs []string) (map[string]any, error) {
	data := make(map[string]any, len(pairs))
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			return nil, fmt.Errorf("invalid --var %q: want key=value", p)
		}
		data[k] = v
	}
	return data, nil
}

// normalizePromptContextDir resolves the complete Setup through its owning
// commons-db type before providers see the request.
func normalizePromptContextDir(req *ai.Request, cwd string) error {
	if cwd == "" {
		return fmt.Errorf("working directory is required")
	}
	setup := shell.Setup{}
	if req.Setup != nil {
		setup = *req.Setup
	}
	resolved, err := setup.Resolve(cwd)
	if err != nil {
		return err
	}
	req.Setup = &resolved
	return nil
}

// fallbackModelsFromFlags turns repeated (and optionally comma-separated) --fallback
// values into name-only fallback Models, in the order given.
func fallbackModelsFromFlags(flags []string) []api.Model {
	var out []api.Model
	for _, flag := range flags {
		for _, name := range strings.Split(flag, ",") {
			if name = strings.TrimSpace(name); name != "" {
				out = append(out, api.Model{Name: name})
			}
		}
	}
	return out
}

// firstNonEmpty returns the first non-empty string, or "" when all are empty.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
