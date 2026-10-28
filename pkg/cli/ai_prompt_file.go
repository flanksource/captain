package cli

import (
	"fmt"
	"maps"
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

// parseVars turns repeated --var flags into the template data map:
//
//	key=value    string; "-" binds the piped stdin, "@file"/"@url" its contents
//	key:=json    typed JSON (array, number, bool, object); "@file" reads the JSON
//	key[]=-      list of stdin lines; key[]=@file lists the file's lines
//	key[sub]=v   nested object
//
// Typed and nested pairs are parsed by clicky's HTTPie-style argument parser.
// Every @ reference is read through expandValueRef, never by clicky's own
// unguarded file reads. boundStdin reports that a var consumed stdin, so the
// caller does not also append it.
func parseVars(pairs []string, stdin string) (data map[string]any, boundStdin bool, err error) {
	data = make(map[string]any, len(pairs))
	var typed []string
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			return nil, false, fmt.Errorf("invalid --var %q: want key=value, key:=json or key[]=@file", p)
		}
		switch {
		case strings.HasSuffix(k, "[]"):
			key := strings.TrimSuffix(k, "[]")
			if strings.ContainsAny(key, "[]:") {
				return nil, false, fmt.Errorf("invalid --var %q: a list var needs a plain key", p)
			}
			lines, err := varLines(k, v, stdin)
			if err != nil {
				return nil, false, err
			}
			data[key], boundStdin = lines, boundStdin || v == "-"
		case strings.HasSuffix(k, ":"):
			if strings.HasPrefix(v, "@") {
				if v, err = expandValueRef(v); err != nil {
					return nil, false, fmt.Errorf("--var %s: %w", k, err)
				}
			}
			typed = append(typed, k+"="+v)
		case strings.Contains(k, "["):
			typed = append(typed, p)
		case v == "-":
			if strings.TrimSpace(stdin) == "" {
				return nil, false, fmt.Errorf("--var %s=- reads stdin, but nothing was piped in", k)
			}
			data[k], boundStdin = strings.TrimSpace(stdin), true
		default:
			if data[k], err = expandValueRef(v); err != nil {
				return nil, false, fmt.Errorf("--var %s: %w", k, err)
			}
		}
	}
	if len(typed) == 0 {
		return data, boundStdin, nil
	}
	parsed, headers, query, err := clicky.ParseArgumentsComplete(typed)
	if err != nil {
		return nil, false, fmt.Errorf("--var: %w", err)
	}
	if len(headers) > 0 || len(query) > 0 {
		return nil, false, fmt.Errorf("invalid --var %v: want key:=json or key[sub]=value", typed)
	}
	maps.Copy(data, parsed)
	return data, boundStdin, nil
}

// varLines reads a key[]= value as a list of its nonblank, non-# lines, from
// piped stdin ("-") or an @file/@url reference.
func varLines(key, value, stdin string) ([]string, error) {
	content := stdin
	switch {
	case value == "-":
		if strings.TrimSpace(stdin) == "" {
			return nil, fmt.Errorf("--var %s=- reads stdin, but nothing was piped in", key)
		}
	case strings.HasPrefix(value, "@"):
		var err error
		if content, err = expandValueRef(value); err != nil {
			return nil, fmt.Errorf("--var %s: %w", key, err)
		}
	default:
		return nil, fmt.Errorf("--var %s=%s: a list var reads - (stdin) or @file", key, value)
	}
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	return lines, nil
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
