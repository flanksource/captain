package promptrun

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/flanksource/commons-db/shell"
	"github.com/google/uuid"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
	aitools "github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/ai/tools/exectool"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/hookaction"
)

// hookActions runs a run's hook actions: a tool through the run's bound caller
// tools (the inline-tool runner, so its output schema, timeout and retry
// apply), a prompt as a child run of the same configuration.
type hookActions struct {
	tools  []api.ToolDefinition
	parent Input
}

func newHookActions(in Input) *hookActions {
	return &hookActions{tools: in.Config.Tools, parent: in}
}

// RunTool runs a bound tool under the same permission a model's call would
// get: the tool's own permission and hints, then the spec's toolPreferences
// and toolPolicy. A deny refuses, an ask goes to the run's approval callback
// and is refused when there is none. This holds even on a runtime that
// ignores tools, whose admission never resolved their permissions.
func (h *hookActions) RunTool(ctx context.Context, name string, input map[string]any) (any, error) {
	idx := slices.IndexFunc(h.tools, func(tool api.ToolDefinition) bool { return tool.Name == name })
	if idx < 0 {
		return nil, fmt.Errorf("tool %q is not bound to this run", name)
	}
	spec := h.parent.Resolved.Spec
	policies, err := aitools.ResolveToolPermissions(h.tools, aitools.ResolveOptions{Preferences: spec.ToolPreferences, Policy: spec.ToolPolicy})
	if err != nil {
		return nil, err
	}
	switch policies[name] {
	case api.ToolPolicyAllow:
	case api.ToolPolicyAsk:
		if input, err = h.approve(ctx, name, input); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("tool %q is denied by the run's tool permissions", name)
	}
	return h.tools[idx].Handler(ctx, input)
}

func (h *hookActions) approve(ctx context.Context, name string, input map[string]any) (map[string]any, error) {
	binding, err := h.parent.Config.Approvals()
	if err != nil {
		return nil, err
	}
	if binding.Func == nil {
		return nil, fmt.Errorf("tool %q needs approval, but the run attaches no approval broker; set permission: allow on the tool or attach one", name)
	}
	decision, err := binding.Func(ctx, api.ApprovalRequest{
		Tool: name, Input: input, Kind: api.ApprovalKindTool, Reason: "hook action",
		ToolUseID: "hook-" + uuid.NewString(), ToolUseIDGenerated: true,
	})
	if err != nil {
		return nil, fmt.Errorf("tool %q approval: %w", name, err)
	}
	if !decision.Allow {
		return nil, fmt.Errorf("tool %q was not approved: %s", name, decision.Message)
	}
	if decision.UpdatedInput != nil {
		return decision.UpdatedInput, nil
	}
	return input, nil
}

// RunPrompt renders the child .prompt with vars and runs it through Run in cwd.
// A child that errors or fails its verification fails the action; a child its
// input hooks skip returns their *Skip as the error, without running. A child
// of a recorded run is recorded as that run's child (see childRecording).
func (h *hookActions) RunPrompt(ctx context.Context, path string, vars map[string]any, cwd string) (any, error) {
	child, skip, err := h.childInput(ctx, path, vars, cwd)
	if err != nil {
		return nil, err
	}
	if skip != nil {
		return nil, skip
	}
	result, err := Run(ctx, child)
	if err != nil {
		return nil, fmt.Errorf("child prompt %s: %w", path, err)
	}
	if !result.Passed {
		return nil, fmt.Errorf("child prompt %s did not pass its verification", path)
	}
	if result.StructuredData != nil {
		return hookaction.Native(result.StructuredData)
	}
	return result.Response.Text, nil
}

func (h *hookActions) childInput(ctx context.Context, path string, vars map[string]any, cwd string) (Input, *Skip, error) {
	template, err := promptlib.LoadFile(path)
	if err != nil {
		return Input{}, nil, err
	}
	spec, cfg, skip, err := RenderTemplate(ctx, template, promptlib.RenderOptions{Data: vars}, h.parent.Config)
	if err != nil || skip != nil {
		return Input{}, skip, err
	}
	if spec.Setup == nil {
		spec.Setup = &shell.Setup{}
	}
	if spec.Setup.Cwd == "" {
		spec.Setup.Cwd = cwd
	}
	record, err := h.childRecording(ctx, path, spec)
	if err != nil {
		return Input{}, nil, err
	}
	return Input{
		Resolved: api.ResolvedSpec{Spec: spec}, Config: childConfig(h.parent.Config, cfg),
		Provider: h.parent.Provider, OnEvent: h.parent.OnEvent, Timeout: h.parent.Timeout, Record: record,
	}, nil, nil
}

// childRecording files a child prompt of a recorded run as its own run with
// parent_run_id set, in a new agent session under the parent run's session,
// so it shares the parent's root. An unrecorded parent records no child.
func (h *hookActions) childRecording(ctx context.Context, path string, spec api.Spec) (*Recording, error) {
	parent := h.parent.recorder
	if parent == nil || h.parent.Record == nil {
		return nil, nil
	}
	session, err := parent.db.GetSession(ctx, parent.sessionID)
	if err != nil {
		return nil, fmt.Errorf("child prompt %s: parent session %s: %w", path, parent.sessionID, err)
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	runID := parent.runID
	return &Recording{
		DB: parent.db,
		Placement: Placement{Sessions: []database.CreateSessionInput{{
			ID: uuid.New(), Source: "captain", Provider: providerName(spec.Provider), HostID: session.HostID,
			ParentSessionID: &session.ID, ParentRelation: database.SessionParentRelationAgent,
			Project: session.Project, CWD: spec.Cwd(), Title: name, InitialPrompt: spec.Prompt.User, AgentType: "prompt",
		}}},
		ParentRunID: &runID, Origin: h.parent.Record.Origin, SpecProfile: name, PromptMarkdown: spec.Prompt.User,
		Runtime: database.PromptRunRuntime{Mode: h.parent.Record.Runtime.Mode, Driver: "prompt"},
	}, nil
}

// childConfig is the parent's configuration for the child's model: the
// parent's caller tools and sessions stay with the parent, its approval broker
// answers the child's asks too, and its credentials go only to a child on the
// same provider.
func childConfig(parent, child ai.Config) ai.Config {
	cfg := parent
	cfg.Model, cfg.Budget = child.Model, child.Budget
	cfg.Tools, cfg.CallerTools = nil, nil
	cfg.SessionID, cfg.CaptainSessionID = "", ""
	if providerName(parent.Model.Provider) != providerName(child.Model.Provider) {
		cfg.APIKey, cfg.APIURL = "", ""
	}
	return cfg
}

func providerName(provider *api.ModelProvider) string {
	if provider == nil {
		return ""
	}
	return provider.Name
}

// schemaHooks is the PreRun that applies output.hooks to the run's request,
// after the inline tools' environments are prepared and before the recorder
// files the spec, so every turn and the record see the emitted schema.
type schemaHooks struct{ actions hookaction.Runner }

func (s *schemaHooks) Name() string { return "schema-hooks" }

func (s *schemaHooks) PreRun(hc *agent.HookContext) error {
	return hookaction.ApplySchemaHooks(hc, hc.Request, s.actions, func(text string) { hc.Notify("%s", text) })
}

// RenderSchemaHooks applies spec's output.hooks outside a run, for a render
// that previews the emitted schema: the hooks' tools run once in a workspace
// of their own, torn down before it returns.
func RenderSchemaHooks(ctx context.Context, spec *api.Spec, cfg ai.Config) error {
	if len(spec.Prompt.SchemaHooks) == 0 {
		return nil
	}
	return withHookWorkspace(ctx, "output.hooks", spec, spec.Prompt.SchemaHooks, cfg, func(runner hookaction.Runner) error {
		return hookaction.ApplySchemaHooks(ctx, spec, runner, nil)
	})
}

// Skip is an input hook's request not to run the prompt.
type Skip = hookaction.Skip

// RenderTemplate renders tmpl the way every render path must once a prompt may
// declare input.hooks: the frontmatter renders with opts.Data, which yields the
// tools and the hooks; the hooks run over those vars in a workspace of their
// own; and the template renders again with the vars they returned, so the body,
// `with` values and tool exec lines all see them. A hook that asks to skip
// returns its Skip with the first render's request.
func RenderTemplate(ctx context.Context, tmpl *promptlib.Template, opts promptlib.RenderOptions, cfg ai.Config) (ai.Request, ai.Config, *Skip, error) {
	req, rendered, err := tmpl.Render(opts)
	if err != nil || len(req.Prompt.InputHooks) == 0 {
		return req, rendered, nil, err
	}
	var skip *Skip
	err = withHookWorkspace(ctx, "input.hooks", &req, req.Prompt.InputHooks, cfg, func(runner hookaction.Runner) error {
		var applyErr error
		skip, applyErr = hookaction.ApplyInputHooks(ctx, &req, runner, nil)
		return applyErr
	})
	if err != nil || skip != nil {
		return req, rendered, skip, err
	}
	opts.Data = req.Prompt.Variables
	req, rendered, err = tmpl.Render(opts)
	return req, rendered, nil, err
}

// withHookWorkspace runs fn with a runner over the tools hooks name, prepared
// in a workspace of their own and torn down before it returns.
func withHookWorkspace(ctx context.Context, field string, spec *api.Spec, hooks []api.HookAction, cfg ai.Config, fn func(hookaction.Runner) error) error {
	tools := api.InlineTools{}
	for _, hook := range hooks {
		if tool, ok := spec.Tools[hook.Tool]; ok {
			tools[hook.Tool] = tool
		}
	}
	workspace := exectool.NewWorkspace(exectool.WorkspaceOptions{Cwd: spec.Cwd(), Setup: spec.Setup})
	definitions, err := exectool.Definitions(tools, workspace)
	if err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	if err := workspace.Prepare(ctx); err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	cfg.Tools = definitions
	err = fn(newHookActions(Input{Resolved: api.ResolvedSpec{Spec: *spec}, Config: cfg}))
	if closeErr := workspace.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	return err
}
