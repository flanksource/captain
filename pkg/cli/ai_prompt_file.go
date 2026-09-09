package cli

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/clicky"
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

// fileRefValue exists so a --var value can be expanded by clicky itself rather
// than by a hand-rolled os.ReadFile. clicky gates `@` expansion on a struct tag
// and refuses credential stores, private keys and kernel state; a second reader
// here would quietly not do that, and `-V key=@~/.ssh/id_rsa` would succeed
// where `-p @~/.ssh/id_rsa` is refused.
type fileRefValue struct {
	Value string `flag:"value" clicky:"cli-file-read"`
}

// expandValueRef resolves an `@file` or `@url` value; anything else is returned
// unchanged.
func expandValueRef(raw string) (string, error) {
	if _, ok := promptFileRef(raw); !ok {
		return raw, nil
	}
	out, err := clicky.BuildOpts[fileRefValue](map[string]string{"value": raw})
	if err != nil {
		return "", err
	}
	return out.Value, nil
}

// parseVars turns repeated --var key=value flags into the template data map.
// A value of "-" binds the piped stdin (reported back so the caller does not
// also append it), and an "@file"/"@url" value is expanded to its contents.
func parseVars(pairs []string, stdin string) (data map[string]any, boundStdin bool, err error) {
	data = make(map[string]any, len(pairs))
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			return nil, false, fmt.Errorf("invalid --var %q: want key=value", p)
		}
		if v == "-" {
			if strings.TrimSpace(stdin) == "" {
				return nil, false, fmt.Errorf("--var %s=- reads stdin, but nothing was piped in", k)
			}
			data[k], boundStdin = strings.TrimSpace(stdin), true
			continue
		}
		if data[k], err = expandValueRef(v); err != nil {
			return nil, false, fmt.Errorf("--var %s: %w", k, err)
		}
	}
	return data, boundStdin, nil
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
