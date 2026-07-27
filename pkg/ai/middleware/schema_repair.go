package middleware

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/api"
)

//go:embed schema_repair.prompt
var schemaRepairPrompts embed.FS

const defaultSchemaRepairPrompt = "schema_repair.prompt"

func (v *validatingProvider) executeRepair(ctx context.Context, parent ai.Request, schema json.RawMessage, verrs string, prev *ai.Response, attempt int) (*ai.Response, error) {
	req, cfg, useParent, err := v.repairRequest(parent, schema, verrs, prev, attempt)
	if err != nil {
		return nil, err
	}
	if useParent {
		return v.provider.Execute(ctx, req)
	}
	p, err := ai.NewProvider(cfg)
	if err != nil {
		return nil, err
	}
	if c, ok := p.(io.Closer); ok {
		defer func() { _ = c.Close() }()
	}
	return p.Execute(ctx, req)
}

func (v *validatingProvider) repairRequest(parent ai.Request, schema json.RawMessage, verrs string, prev *ai.Response, attempt int) (ai.Request, ai.Config, bool, error) {
	tmpl, err := v.repairTemplate()
	if err != nil {
		return ai.Request{}, ai.Config{}, false, err
	}
	renderedReq, renderedCfg, err := tmpl.Render(map[string]any{
		"attempt":          attempt,
		"backend":          string(v.provider.GetBackend()),
		"model":            v.provider.GetModel(),
		"originalPrompt":   parent.Prompt.User,
		"previousResponse": responseJSON(prev),
		"schema":           string(schema),
		"validationErrors": verrs,
	}, nil)
	if err != nil {
		return ai.Request{}, ai.Config{}, false, fmt.Errorf("render schema repair prompt: %w", err)
	}

	parentModel := parent.Model
	if parentModel.Name == "" {
		parentModel.Name = v.provider.GetModel()
	}
	if parentModel.Backend == "" {
		parentModel.Backend = v.provider.GetBackend()
	}

	model := parentModel
	model = overlayModel(model, renderedCfg.Model)
	model = overlayModel(model, renderedReq.Model)
	model = overlayModel(model, v.cfg.SchemaRepair.Model)

	req := parent
	req.Model = model
	req.Prompt = renderedReq.Prompt
	req.Prompt.Schema = parent.Prompt.Schema
	req.Prompt.SchemaJSON = parent.Prompt.SchemaJSON
	req.Prompt.SchemaStrictness = api.SchemaStrictnessDisabled
	req.Workflow = nil

	cfg := v.cfg
	cfg.Model = model
	cfg.SchemaRepair = api.SchemaRepairConfig{}
	if model.Backend != "" && model.Backend != v.provider.GetBackend() {
		cfg.APIKey = ""
	}

	return req, cfg, sameModelBackend(model, parentModel), nil
}

func (v *validatingProvider) repairTemplate() (*promptlib.Template, error) {
	if path := strings.TrimSpace(v.cfg.SchemaRepair.Prompt); path != "" {
		return promptlib.LoadFile(path)
	}
	return promptlib.LoadFS(schemaRepairPrompts, defaultSchemaRepairPrompt)
}

func overlayModel(base, overlay api.Model) api.Model {
	if overlay.Name != "" {
		base.Name = overlay.Name
	}
	if overlay.ID != "" {
		base.ID = overlay.ID
	}
	if overlay.Backend != "" {
		base.Backend = overlay.Backend
	}
	if overlay.Temperature != nil {
		base.Temperature = overlay.Temperature
	}
	if overlay.Effort != "" {
		base.Effort = overlay.Effort
	}
	if overlay.NoCache {
		base.NoCache = true
	}
	if len(overlay.Fallbacks) > 0 {
		base.Fallbacks = overlay.Fallbacks
	}
	return base
}

func sameModelBackend(a, b api.Model) bool {
	return strings.TrimSpace(a.Name) == strings.TrimSpace(b.Name) &&
		strings.TrimSpace(a.ID) == strings.TrimSpace(b.ID) &&
		a.Backend == b.Backend
}
