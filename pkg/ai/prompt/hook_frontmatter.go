package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"gopkg.in/yaml.v3"
)

// hookRoots are the variables only a hook has: the run's vars and metadata as
// hook inputs, and what a lifecycle hook reads of the finished run. A
// frontmatter expression over one of them is left for the hook to render.
var hookRoots = []string{"vars", "metadata", "output", "result", "steps", "outcome"}

var (
	frontmatterMustache = regexp.MustCompile(`\\?\{\{\{?[^{}]*\}?\}\}`)
	mustacheToken       = regexp.MustCompile(`[\s()=]+`)
)

// deferHookExpressions escapes every mustache expression in frontmatter that
// reads a hook-only variable, so the frontmatter render emits it verbatim for
// the hook that owns it to render later.
func deferHookExpressions(frontmatter string) string {
	return frontmatterMustache.ReplaceAllStringFunc(frontmatter, func(expr string) string {
		if strings.HasPrefix(expr, `\`) || !readsHookRoot(expr) {
			return expr
		}
		return `\` + expr
	})
}

func readsHookRoot(expr string) bool {
	inner := strings.Trim(expr, "{}~ ")
	for _, token := range mustacheToken.Split(inner, -1) {
		token = strings.TrimLeft(token, "#/^&~")
		for _, root := range hookRoots {
			if token == root || strings.HasPrefix(token, root+".") || strings.HasPrefix(token, root+"[") {
				return true
			}
		}
	}
	return false
}

// decodeSectionHooks reads <section>.hooks — output.hooks or input.hooks —
// which dotprompt's input and output blocks do not model.
func decodeSectionHooks(raw map[string]any, section string) ([]api.HookAction, error) {
	block, ok := raw[section].(map[string]any)
	if !ok || block["hooks"] == nil {
		return nil, nil
	}
	encoded, err := yaml.Marshal(block["hooks"])
	if err != nil {
		return nil, fmt.Errorf("re-encode %s.hooks: %w", section, err)
	}
	var hooks []api.HookAction
	if err := yaml.Unmarshal(encoded, &hooks); err != nil {
		return nil, fmt.Errorf("%s.hooks: %w", section, err)
	}
	return hooks, nil
}

// declaresHooks reports whether anything in req renders hook variables.
func declaresHooks(req ai.Request) bool {
	return len(req.Prompt.SchemaHooks) > 0 || len(req.Prompt.InputHooks) > 0 || (req.Workflow != nil && len(req.Workflow.After) > 0)
}

type hookPromptField struct {
	name string
	path *string
}

// hookPromptFields lists every child prompt path a hook in req declares, in
// frontmatter order, with the field that declares it.
func hookPromptFields(req *ai.Request) []hookPromptField {
	var fields []hookPromptField
	for i := range req.Prompt.InputHooks {
		fields = append(fields, hookPromptField{fmt.Sprintf("input.hooks[%d].prompt", i), &req.Prompt.InputHooks[i].Prompt})
	}
	for i := range req.Prompt.SchemaHooks {
		fields = append(fields, hookPromptField{fmt.Sprintf("output.hooks[%d].prompt", i), &req.Prompt.SchemaHooks[i].Prompt})
	}
	if req.Workflow != nil {
		for i := range req.Workflow.After {
			fields = append(fields, hookPromptField{fmt.Sprintf("workflow.after[%d].prompt", i), &req.Workflow.After[i].Prompt})
		}
	}
	return fields
}

// resolveHookPrompts makes every relative child prompt path absolute against
// dir, the directory of the prompt that declares it.
func resolveHookPrompts(req *ai.Request, dir string) error {
	resolve := func(field string, path *string) error {
		if *path == "" || filepath.IsAbs(*path) {
			return nil
		}
		if dir == "" {
			return fmt.Errorf("%s %q is relative, but the prompt was not loaded from a directory to resolve it against", field, *path)
		}
		abs, err := filepath.Abs(filepath.Join(dir, *path))
		if err != nil {
			return fmt.Errorf("%s %q: %w", field, *path, err)
		}
		*path = abs
		return nil
	}
	for _, field := range hookPromptFields(req) {
		if err := resolve(field.name, field.path); err != nil {
			return err
		}
	}
	return nil
}

// validateHookPrompts confirms every child prompt a hook names exists.
func validateHookPrompts(spec api.Spec, dir string) error {
	req := ai.Request(spec)
	req.Prompt.SchemaHooks = slices.Clone(req.Prompt.SchemaHooks)
	req.Prompt.InputHooks = slices.Clone(req.Prompt.InputHooks)
	if req.Workflow != nil {
		workflow := *req.Workflow
		workflow.After = slices.Clone(workflow.After)
		req.Workflow = &workflow
	}
	if err := resolveHookPrompts(&req, dir); err != nil {
		return err
	}
	for _, field := range hookPromptFields(&req) {
		if info, err := os.Stat(*field.path); *field.path != "" && (err != nil || info.IsDir()) {
			return fmt.Errorf("%s %q: not a readable prompt file", field.name, *field.path)
		}
	}
	return nil
}
