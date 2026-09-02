package cli

import (
	"context"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/runtimeprofiles"
	"github.com/flanksource/clicky"
	clickyapi "github.com/flanksource/clicky/api"
)

type RuntimePresetListOptions struct {
	Query  string `flag:"query" help:"Search preset name or description"`
	Source string `flag:"source" help:"Filter by source: db|file|<source-id>"`
	Scope  string `flag:"scope" help:"Filter by scope: global|context|surface|user"`
}

// RuntimePresetRecord is a catalog preset as the entity lists and returns it:
// the whole record, so a listing never needs a second fetch per row.
type RuntimePresetRecord struct {
	runtimeprofiles.Preset
}

func (r RuntimePresetRecord) GetID() string   { return r.ID }
func (r RuntimePresetRecord) GetName() string { return r.Name }

func (r RuntimePresetRecord) Columns() []clickyapi.ColumnDef {
	return []clickyapi.ColumnDef{
		clickyapi.Column("name").Label("Name").Build(),
		clickyapi.Column("source").Label("Source").MaxWidth(40).Build(),
		clickyapi.Column("scope").Label("Scope").Build(),
		clickyapi.Column("model").Label("Model").Build(),
		clickyapi.Column("description").Label("Description").MaxWidth(80).Build(),
	}
}

func (r RuntimePresetRecord) Row() map[string]any {
	return map[string]any{
		"name":        r.Name,
		"source":      r.Source.Label,
		"scope":       string(r.Scope),
		"model":       runtimeModelLabel(r.Spec.Model),
		"description": r.Description,
	}
}

// RuntimePresetWriteRequest is the create/update body. Content is the whole
// record as YAML (the file form) for the CLI path
// (`captain runtime-preset create content@preset.yaml`) and excludes the
// structured fields. Target is honoured on create only: a record never moves
// between sources. ID is how the executor routes an update
// (PUT /api/v1/runtime-preset with the id in the body) and is refused on create.
type RuntimePresetWriteRequest struct {
	ID          string                 `json:"id,omitempty"`
	Target      string                 `json:"target,omitempty"`
	Name        string                 `json:"name,omitempty"`
	Description string                 `json:"description,omitempty"`
	Scope       api.SpecLayerScope     `json:"scope,omitempty"`
	Spec        *api.RuntimePresetSpec `json:"spec,omitempty"`
	Content     string                 `json:"content,omitempty"`
}

func (req RuntimePresetWriteRequest) input() (runtimeprofiles.PresetInput, error) {
	if req.Content == "" {
		in := runtimeprofiles.PresetInput{Name: req.Name, Description: req.Description, Scope: req.Scope}
		if req.Spec != nil {
			in.Spec = *req.Spec
		}
		return in, nil
	}
	if req.Name != "" || req.Description != "" || req.Scope != "" || req.Spec != nil {
		return runtimeprofiles.PresetInput{}, runtimeBodyError("content excludes name, description, scope and spec; send one or the other")
	}
	var in runtimeprofiles.PresetInput
	if err := decodeRuntimeContent(req.Content, &in); err != nil {
		return runtimeprofiles.PresetInput{}, err
	}
	return in, nil
}

func registerRuntimePresetEntity() {
	clicky.NewEntity[RuntimePresetRecord, RuntimePresetListOptions, RuntimePresetRecord]("runtime-preset").
		Aliases("runtime-presets").
		ToolGroup(runtimeToolGroup).
		ListWithContext(listRuntimePresets).
		GetWithContext(getRuntimePreset).
		CreateWithContext(createRuntimePreset).
		UpdateWithContext(updateRuntimePreset).
		DeleteWithContext(deleteRuntimePreset).
		Register()
}

func listRuntimePresets(ctx context.Context, opts RuntimePresetListOptions) ([]RuntimePresetRecord, error) {
	scope, err := runtimeScopeFilter(opts.Scope)
	if err != nil {
		return nil, err
	}
	catalog, err := buildRuntimeCatalog(ctx)
	if err != nil {
		return nil, err
	}
	presets, err := catalog.ListPresets(ctx)
	if err != nil {
		return nil, runtimeCatalogError(err)
	}
	out := []RuntimePresetRecord{}
	for _, preset := range presets {
		if scope != "" && preset.Scope != scope {
			continue
		}
		if !runtimeSourceMatches(preset.Source, opts.Source) || !runtimeQueryMatches(opts.Query, preset.Name, preset.Description) {
			continue
		}
		out = append(out, RuntimePresetRecord{Preset: preset})
	}
	sortRuntimeRecords(out, func(record RuntimePresetRecord) (runtimeprofiles.SourceKind, string) {
		return record.Source.Kind, record.Name
	})
	return out, nil
}

func getRuntimePreset(ctx context.Context, id string) (RuntimePresetRecord, error) {
	catalog, err := buildRuntimeCatalog(ctx)
	if err != nil {
		return RuntimePresetRecord{}, err
	}
	preset, err := catalog.GetPreset(ctx, id)
	if err != nil {
		return RuntimePresetRecord{}, runtimeCatalogError(err)
	}
	return RuntimePresetRecord{Preset: preset}, nil
}

func createRuntimePreset(ctx context.Context, body map[string]any) (RuntimePresetRecord, error) {
	var req RuntimePresetWriteRequest
	if err := decodeRuntimeBody(ctx, body, &req); err != nil {
		return RuntimePresetRecord{}, err
	}
	if err := requireNoRuntimeID(req.ID); err != nil {
		return RuntimePresetRecord{}, err
	}
	in, err := req.input()
	if err != nil {
		return RuntimePresetRecord{}, err
	}
	catalog, err := buildRuntimeCatalog(ctx)
	if err != nil {
		return RuntimePresetRecord{}, err
	}
	if err := requireRuntimeTarget(catalog, req.Target); err != nil {
		return RuntimePresetRecord{}, err
	}
	preset, err := catalog.CreatePreset(ctx, req.Target, in)
	if err != nil {
		return RuntimePresetRecord{}, runtimeCatalogError(err)
	}
	return RuntimePresetRecord{Preset: preset}, nil
}

func updateRuntimePreset(ctx context.Context, id string, body map[string]any) (RuntimePresetRecord, error) {
	var req RuntimePresetWriteRequest
	if err := decodeRuntimeBody(ctx, body, &req); err != nil {
		return RuntimePresetRecord{}, err
	}
	if err := requireRuntimeIDMatch(req.ID, id); err != nil {
		return RuntimePresetRecord{}, err
	}
	in, err := req.input()
	if err != nil {
		return RuntimePresetRecord{}, err
	}
	catalog, err := buildRuntimeCatalog(ctx)
	if err != nil {
		return RuntimePresetRecord{}, err
	}
	preset, err := catalog.UpdatePreset(ctx, id, in)
	if err != nil {
		return RuntimePresetRecord{}, runtimeCatalogError(err)
	}
	return RuntimePresetRecord{Preset: preset}, nil
}

func deleteRuntimePreset(ctx context.Context, id string) error {
	catalog, err := buildRuntimeCatalog(ctx)
	if err != nil {
		return err
	}
	return runtimeCatalogError(catalog.DeletePreset(ctx, id))
}
