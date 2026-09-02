package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude"
	clickyrpc "github.com/flanksource/clicky/rpc"
)

// readStdinIfCLI returns piped stdin, but only on the CLI path — there is no
// process stdin over HTTP.
func readStdinIfCLI(ctx context.Context) string {
	if _, isHTTP := clickyrpc.RequestFromContext(ctx); isHTTP {
		return ""
	}
	if !claude.IsStdinPiped() {
		return ""
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return ""
	}
	return string(b)
}

// loadPromptContent resolves the prompt source for the unified prompt commands.
// Precedence: the positional (a discovered name, .prompt filepath, or registry
// id) > --prompt/-p text > piped stdin. usedStdin reports whether stdin became
// the prompt body (so the caller does not also expose it as the {{input}}
// variable).
func loadPromptContent(ctx context.Context, id string, opts AIPromptOptions, stdin string) (content, source string, usedStdin bool, record promptRecord, err error) {
	switch {
	case strings.TrimSpace(id) != "":
		record, err := resolvePromptRecord(ctx, id)
		if err != nil {
			return "", "", false, promptRecord{}, err
		}
		if record.Source.Kind == "file" {
			log.Debugf("prompt source: file %s (positional %q)", record.Path, id)
		} else {
			log.Debugf("prompt source: resolved %q → %s/%s", id, record.Source.Kind, record.Rel)
		}
		c, err := readPromptContent(record)
		if err != nil {
			return "", "", false, promptRecord{}, err
		}
		return c, record.Rel, false, record, nil
	case opts.Prompt != "":
		log.Debugf("prompt source: inline --prompt/-p (%d chars)", len(opts.Prompt))
		return opts.Prompt, "<inline>", false, promptRecord{Rel: "inline.prompt"}, nil
	case strings.TrimSpace(stdin) != "":
		log.Debugf("prompt source: stdin (%d chars)", len(stdin))
		return stdin, "<stdin>", true, promptRecord{Rel: "stdin.prompt"}, nil
	case len(opts.Attach) > 0:
		return ephemeralPromptContent(), "<attachment>", false, promptRecord{Rel: "attachment.prompt"}, nil
	default:
		return "", "", false, promptRecord{}, fmt.Errorf("prompt or attachment required: pass a prompt name, .prompt file, id, --prompt/-p text, --attach/-A, or pipe via stdin")
	}
}

// promptVars builds the template data from --var key=value pairs and a --vars
// JSON blob; unused stdin is exposed as {{input}}.
func promptVars(opts AIPromptOptions, varsJSON, stdin string, usedStdin bool) (map[string]any, error) {
	data := map[string]any{}
	if s := strings.TrimSpace(varsJSON); s != "" {
		if err := json.Unmarshal([]byte(s), &data); err != nil {
			return nil, fmt.Errorf("parse --vars JSON: %w", err)
		}
	}
	kv, err := parseVars(opts.Var)
	if err != nil {
		return nil, err
	}
	for k, v := range kv {
		data[k] = v
	}
	if s := strings.TrimSpace(stdin); s != "" && !usedStdin {
		data["input"] = s
	}
	return data, nil
}

// renderLoadedContent renders already-loaded .prompt content with vars, layers
// the selected runtime profile (--runtime-profile, else the frontmatter pin)
// beneath the frontmatter, overlays the CLI options, tags the source, and
// normalizes the context dir. The flags are not a spec layer yet: overlayCLI
// also folds saved defaults, API keys and the sandbox selection, none of which
// are Spec fields, so it stays the step above the resolved layers.
func renderLoadedContent(ctx context.Context, content, source string, vars map[string]any, opts AIPromptOptions) (ai.Request, ai.Config, api.ResolvedSpec, error) {
	frontmatter, _, err := promptlib.Load(content).Render(vars, nil)
	if err != nil {
		return ai.Request{}, ai.Config{}, api.ResolvedSpec{}, err
	}
	frontmatter.Prompt.Source = source
	resolved, err := resolveRenderLayers(ctx, source, content, frontmatter, PromptRenderRequest{RuntimeProfile: opts.RuntimeProfile})
	if err != nil {
		return ai.Request{}, ai.Config{}, api.ResolvedSpec{}, err
	}
	layered := resolved.Spec
	foldSkillPolicies(&layered)
	req, cfg, err := overlayCLI(layered, configFromResolved(layered), opts)
	if err != nil {
		return ai.Request{}, ai.Config{}, api.ResolvedSpec{}, err
	}
	req.Prompt.Source = source
	cwd, err := os.Getwd()
	if err != nil {
		return ai.Request{}, ai.Config{}, api.ResolvedSpec{}, fmt.Errorf("get working directory: %w", err)
	}
	if err := normalizePromptContextDir(&req, cwd); err != nil {
		return ai.Request{}, ai.Config{}, api.ResolvedSpec{}, err
	}
	return req, cfg, resolved, nil
}

// actionFlagsToOptions reconstructs the typed AIPromptOptions from the entity
// action's stringly-typed flag map (clicky CSV-encodes []string and "true"/"false"
// for bool), so the render/run core can reuse overlayCLI.
func actionFlagsToOptions(f map[string]string) (AIPromptOptions, error) {
	var o AIPromptOptions
	o.Model = f["model"]
	o.Fallback = flagSlice(f["fallback"])
	o.Mode = f["mode"]
	o.RuntimeProfile = f["runtime-profile"]
	o.APIKey = f["api-key"]
	o.APIURL = f["api-url"]
	o.NoCache = flagBool(f["no-cache"])
	o.Budget = f["budget"]
	o.Sandbox = f["sandbox"]
	mt, err := flagInt("max-tokens", f["max-tokens"])
	if err != nil {
		return o, err
	}
	o.MaxTokens = mt
	o.Temperature = f["temperature"]
	o.Effort = f["effort"]
	turns, err := flagInt("max-turns", f["max-turns"])
	if err != nil {
		return o, err
	}
	o.MaxTurns = turns
	o.Resume = f["resume"]
	o.Edit = flagBool(f["edit"])
	o.AllowedTools = flagSlice(f["allowed-tools"])
	o.DisallowedTools = flagSlice(f["disallowed-tools"])
	o.PermissionMode = f["permission-mode"]
	o.NoMCP = flagBool(f["no-mcp"])
	o.NoHooks = flagBool(f["no-hooks"])
	o.NoSkills = flagBool(f["no-skills"])
	o.SkillDirs = flagSlice(f["skill-dir"])
	o.NoUser = flagBool(f["no-user"])
	o.NoProject = flagBool(f["no-project"])
	o.NoMemory = flagBool(f["no-memory"])
	o.Bare = flagBool(f["bare"])
	o.Prompt = f["prompt"]
	o.System = f["system"]
	o.AppendSystem = f["append-system"]
	o.Var = flagSlice(f["var"])
	if attach := strings.TrimSpace(f["attach"]); attach != "" {
		o.Attach = []string{attach}
	}
	o.MultiModels = flagSlice(f["multi-models"])
	o.Timeout = f["timeout"]
	o.NoStream = flagBool(f["no-stream"])
	return o, nil
}

func flagBool(s string) bool { return strings.EqualFold(strings.TrimSpace(s), "true") }

func flagInt(name, s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid --%s %q: %w", name, s, err)
	}
	return n, nil
}

func flagSlice(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
