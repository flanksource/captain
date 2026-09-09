package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/claude"
	clickyrpc "github.com/flanksource/clicky/rpc"
)

// readStdinIfCLI returns piped stdin, but only on the CLI path — there is no
// process stdin over HTTP. It must be called once, before the prompt source is
// chosen: os.Stdin drains on the first read, so a later `-` or `-V key=-` has
// to be served from the captured string rather than by reading again.
func readStdinIfCLI(ctx context.Context) (string, error) {
	if _, isHTTP := clickyrpc.RequestFromContext(ctx); isHTTP {
		return "", nil
	}
	if !claude.IsStdinPiped() {
		return "", nil
	}
	b, err := io.ReadAll(os.Stdin)
	if err != nil {
		return "", fmt.Errorf("read prompt from stdin: %w", err)
	}
	return string(b), nil
}

// isStdinToken reports whether a positional names stdin explicitly, the same
// two spellings `captain history` accepts.
func isStdinToken(id string) bool {
	switch strings.TrimSpace(id) {
	case "-", "/dev/stdin":
		return true
	}
	return false
}

// promptBody is the resolved prompt source.
type promptBody struct {
	Text string
	// Source labels the origin in the layer trace and in render errors — a
	// registry path, "<stdin>", or "<inline>".
	Source string
	// Literal marks caller-supplied data rather than an authored template. A
	// literal body is used verbatim: no frontmatter split, no Handlebars pass.
	// Piping a document that happens to contain "{{" or open with "---" must
	// deliver that document, not fail to parse or silently absorb its head as
	// frontmatter.
	Literal bool
	// UsedStdin reports that piped stdin became the body, so it is neither
	// offered as {{input}} nor appended a second time.
	UsedStdin bool
	Record    promptRecord
}

// loadPromptContent resolves the prompt source for the unified prompt commands.
// Precedence: the positional (a discovered name, .prompt filepath, registry id,
// or `-` for stdin) > --prompt/-p text > piped stdin > --attach.
type promptContentOptions struct {
	ID     string
	Prompt AIPromptOptions
	Stdin  string
	Config *captainconfig.Config
	// HasVars reports that --var/--vars were supplied, which opts ad-hoc text
	// into templating.
	HasVars bool
}

func loadPromptContent(ctx context.Context, options promptContentOptions) (promptBody, error) {
	id, opts, stdin := options.ID, options.Prompt, options.Stdin
	// Ad-hoc text carries {{input}} only when there are piped bytes to bind.
	hasInput := !isStdinToken(id) && strings.TrimSpace(stdin) != ""
	switch {
	case isStdinToken(id):
		if strings.TrimSpace(stdin) == "" {
			return promptBody{}, fmt.Errorf("prompt source %q names stdin, but nothing was piped in", strings.TrimSpace(id))
		}
		log.Debugf("prompt source: stdin via %q (%d chars)", strings.TrimSpace(id), len(stdin))
		return stdinPromptBody(stdin, options.HasVars), nil
	case strings.TrimSpace(id) != "":
		record, err := resolvePromptRecord(ctx, promptRecordOptions{ID: id, Config: options.Config})
		if err != nil {
			return promptBody{}, err
		}
		if record.Source.Kind == "file" {
			log.Debugf("prompt source: file %s (positional %q)", record.Path, id)
		} else {
			log.Debugf("prompt source: resolved %q → %s/%s", id, record.Source.Kind, record.Rel)
		}
		c, err := readPromptContent(record)
		if err != nil {
			return promptBody{}, err
		}
		return promptBody{Text: c, Source: record.Rel, Record: record}, nil
	case isStdinToken(opts.Prompt):
		if strings.TrimSpace(stdin) == "" {
			return promptBody{}, fmt.Errorf("--prompt/-p %q reads stdin, but nothing was piped in", strings.TrimSpace(opts.Prompt))
		}
		log.Debugf("prompt source: stdin via --prompt/-p %q (%d chars)", strings.TrimSpace(opts.Prompt), len(stdin))
		return stdinPromptBody(stdin, options.HasVars), nil
	case opts.Prompt != "":
		log.Debugf("prompt source: inline --prompt/-p (%d chars)", len(opts.Prompt))
		return promptBody{
			Text:    opts.Prompt,
			Source:  opts.promptSourceLabel(),
			Literal: !opts.promptIsTemplate(options.HasVars, hasInput),
			Record:  promptRecord{Rel: "inline.prompt"},
		}, nil
	case strings.TrimSpace(stdin) != "":
		log.Debugf("prompt source: stdin (%d chars)", len(stdin))
		return stdinPromptBody(stdin, options.HasVars), nil
	case len(opts.Attach) > 0:
		return promptBody{Text: ephemeralPromptContent(), Source: "<attachment>", Record: promptRecord{Rel: "attachment.prompt"}}, nil
	default:
		return promptBody{}, fmt.Errorf("prompt or attachment required: pass a prompt name, .prompt file, id, --prompt/-p text, --attach/-A, or pipe via stdin")
	}
}

func stdinPromptBody(stdin string, hasVars bool) promptBody {
	return promptBody{
		Text:      stdin,
		Source:    "<stdin>",
		Literal:   !hasVars,
		UsedStdin: true,
		Record:    promptRecord{Rel: "stdin.prompt"},
	}
}

// inputRefRe matches a Handlebars reference to the `input` variable, covering
// the {{input}}, {{{input}}}, {{#if input}} and {{helper input}} forms.
var inputRefRe = regexp.MustCompile(`\{\{[^}]*\binput\b`)

func referencesInput(text string) bool { return inputRefRe.MatchString(text) }

// promptFileRef reports whether a raw flag value is an `@file`/`@url`
// reference, and returns what it points at. Text after the `@` that contains
// whitespace is prose — "@channel ship it" — not a path, so it is left alone;
// this is the same test used before handing a value to clicky for expansion.
func promptFileRef(raw string) (string, bool) {
	ref, ok := strings.CutPrefix(strings.TrimSpace(raw), "@")
	if !ok || ref == "" || strings.ContainsAny(ref, " \t\r\n") {
		return "", false
	}
	return ref, true
}

// promptSourceLabel names the --prompt/-p source in the layer trace and in
// render errors: the real file behind an @reference, otherwise "<inline>".
func (o AIPromptOptions) promptSourceLabel() string {
	if path, ok := promptFileRef(o.PromptRef); ok {
		return path
	}
	return "<inline>"
}

// promptIsTemplate reports whether --prompt/-p text should be rendered as a
// Handlebars template. An @reference to a .prompt file is authored as one. Any
// other ad-hoc text is data unless the caller supplied variables or asked for
// the {{input}} binding by name.
func (o AIPromptOptions) promptIsTemplate(hasVars, hasInput bool) bool {
	if path, ok := promptFileRef(o.PromptRef); ok && strings.HasSuffix(path, ".prompt") {
		return true
	}
	return hasVars || (hasInput && referencesInput(o.Prompt))
}

// promptVarsResult is the template data plus the one fact the caller cannot
// re-derive from it: whether the piped bytes already found a home.
type promptVarsResult struct {
	Data map[string]any
	// StdinBoundToVar reports that an explicit `-V key=-` consumed stdin, so
	// it must not also be appended to the rendered prompt.
	StdinBoundToVar bool
}

// promptVars builds the template data from --var key=value pairs and a --vars
// JSON blob; unused stdin is exposed as {{input}}.
func promptVars(opts AIPromptOptions, varsJSON, stdin string, usedStdin bool) (promptVarsResult, error) {
	data := map[string]any{}
	if s := strings.TrimSpace(varsJSON); s != "" {
		if err := json.Unmarshal([]byte(s), &data); err != nil {
			return promptVarsResult{}, fmt.Errorf("parse --vars JSON: %w", err)
		}
	}
	kv, boundStdin, err := parseVars(opts.Var, stdin)
	if err != nil {
		return promptVarsResult{}, err
	}
	for k, v := range kv {
		data[k] = v
	}
	if s := strings.TrimSpace(stdin); s != "" && !usedStdin {
		data["input"] = s
	}
	return promptVarsResult{Data: data, StdinBoundToVar: boundStdin}, nil
}

// renderLoadedLayers turns the resolved body into the prompt's spec layer.
//
// A literal body skips the dotprompt pass entirely — it is the caller's data,
// so its braces and any leading "---" are content, not syntax. Piped bytes the
// prompt never consumed are appended here, before resolution, so that every
// downstream reader agrees on what was sent: the layer trace, the persisted
// spec, task labels, and each --multi-models variant (which re-derives its own
// request from the resolved spec and would drop a later mutation).
func renderLoadedLayers(ctx context.Context, body promptBody, vars promptVarsResult, stdin string, opts AIPromptOptions, saved captainconfig.Config) ([]api.SpecLayer, error) {
	frontmatter, err := promptFrontmatter(body, vars, stdin)
	if err != nil {
		return nil, err
	}
	layers, err := renderLayers(ctx, body.Source, body.Text, frontmatter, PromptRenderRequest{RuntimeProfile: opts.RuntimeProfile, Literal: body.Literal}, saved)
	if err != nil {
		return nil, err
	}
	promptFlags, err := opts.promptSpec()
	if err != nil {
		return nil, err
	}
	if len(promptFlags.Prompt.Attachments) > 0 {
		promptFlags.Prompt.Attachments = append(append([]api.AttachmentRef(nil), frontmatter.Prompt.Attachments...), promptFlags.Prompt.Attachments...)
	}
	if len(promptFlags.Fields()) > 0 {
		layers = append(layers, api.RequestSpecLayer("prompt flags", promptFlags))
	}
	return layers, nil
}

// promptFrontmatter turns the resolved body into the prompt's own spec — the
// layer everything else resolves on top of.
func promptFrontmatter(body promptBody, vars promptVarsResult, stdin string) (ai.Request, error) {
	var frontmatter ai.Request
	if body.Literal {
		frontmatter = ai.Request{Prompt: api.Prompt{User: body.Text}}
	} else {
		var err error
		if frontmatter, _, err = promptlib.LoadNamed(body.Source, body.Text).Render(promptlib.RenderOptions{Data: vars.Data, Declared: true}); err != nil {
			return ai.Request{}, err
		}
	}
	appendUnboundStdin(&frontmatter, body, vars, stdin)
	frontmatter.Prompt.Source = body.Source
	return frontmatter, nil
}

// appendUnboundStdin appends piped bytes that nothing consumed. Stdin reaches a
// prompt three ways — as the body, as {{input}}, or bound by `-V key=-` — and a
// prompt that takes none of them used to drop it silently: `git diff | captain
// prompt run commit` sent an empty diff, because commit.prompt declares
// {{patch}} and never mentions {{input}}.
//
// A verify-only spec is left alone: its empty prompt body is what marks it as
// verification rather than generation.
func appendUnboundStdin(req *ai.Request, body promptBody, vars promptVarsResult, stdin string) {
	piped := strings.TrimSpace(stdin)
	consumed := body.UsedStdin || vars.StdinBoundToVar || (!body.Literal && referencesInput(body.Text))
	if piped == "" || consumed || req.IsVerifyOnly() {
		return
	}
	log.Debugf("appending %d chars of unbound stdin to the prompt body", len(piped))
	if existing := strings.TrimSpace(req.Prompt.User); existing != "" {
		req.Prompt.User = existing + "\n\n" + piped
		return
	}
	req.Prompt.User = piped
}

// actionFlagsToOptions reconstructs the typed AIPromptOptions from the entity
// action's stringly-typed flag map (clicky CSV-encodes []string and "true"/"false"
// for bool). Only changed flags are present, including explicit false values.
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
	// The text-bearing flags accept an @file/@url reference. Expansion is
	// clicky's — the `clicky:"cli-file-read"` tag on PromptActionFlags declares
	// the opt-in, but this action receives an already-flattened flag map and so
	// never reaches the typed binding that honours it.
	o.PromptRef = f["prompt"]
	for _, field := range []struct {
		flag string
		dst  *string
	}{
		{"prompt", &o.Prompt},
		{"system", &o.System},
		{"append-system", &o.AppendSystem},
		{"vars", &o.Vars},
	} {
		expanded, err := expandValueRef(f[field.flag])
		if err != nil {
			return o, fmt.Errorf("--%s: %w", field.flag, err)
		}
		*field.dst = expanded
	}
	o.Var = flagSlice(f["var"])
	if attach := strings.TrimSpace(f["attach"]); attach != "" {
		o.Attach = []string{attach}
	}
	o.MultiModels = flagSlice(f["multi-models"])
	o.Timeout = f["timeout"]
	o.NoStream = flagBool(f["no-stream"])
	for flag, path := range runtimeFlagFields {
		if _, present := f[flag]; present {
			o.AIRuntimeOptions = o.WithExplicit(path)
			// Model-describing paths live on the embedded ModelFlags too;
			// WithChangedFlags mirrors both, and so must this map-driven twin,
			// or an explicit --effort "" loses half its presence record.
			switch path {
			case "/model", "/mode", "/effort", "/temperature", "/noCache", "/fallbacks":
				o.ModelFlags = o.ModelFlags.WithExplicit(path)
			}
		}
	}
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
