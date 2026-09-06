package cli

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/runtimeprofiles"
	"github.com/flanksource/clicky"
	clickyapi "github.com/flanksource/clicky/api"
)

type RuntimeProfileListOptions struct {
	Query  string `flag:"query" help:"Search profile name or description"`
	Source string `flag:"source" help:"Filter by source: db|file|<source-id>"`
	Preset string `flag:"preset" help:"Only profiles referencing this preset id or name"`
}

// RuntimeProfileRecord is a catalog profile as the entity lists and returns
// it: the whole record, so a listing never needs a second fetch per row.
type RuntimeProfileRecord struct {
	runtimeprofiles.Profile
}

func (r RuntimeProfileRecord) GetID() string   { return r.ID }
func (r RuntimeProfileRecord) GetName() string { return r.Name }

func (r RuntimeProfileRecord) Columns() []clickyapi.ColumnDef {
	return []clickyapi.ColumnDef{
		clickyapi.Column("name").Label("Name").Build(),
		clickyapi.Column("source").Label("Source").MaxWidth(40).Build(),
		clickyapi.Column("model").Label("Model").Build(),
		clickyapi.Column("presets").Label("Presets").Build(),
		clickyapi.Column("description").Label("Description").MaxWidth(80).Build(),
	}
}

func (r RuntimeProfileRecord) Row() map[string]any {
	return map[string]any{
		"name":        r.Name,
		"source":      r.Source.Label,
		"model":       runtimeModelLabel(r.Spec.Model),
		"presets":     strconv.Itoa(len(r.Presets)),
		"description": r.Description,
	}
}

// RuntimeProfileWriteRequest is the create/update body; see
// RuntimePresetWriteRequest for the ID, Content and Target rules.
type RuntimeProfileWriteRequest struct {
	ID          string    `json:"id,omitempty"`
	Target      string    `json:"target,omitempty"`
	Name        string    `json:"name,omitempty"`
	Description string    `json:"description,omitempty"`
	Spec        *api.Spec `json:"spec,omitempty"`
	Presets     []string  `json:"presets,omitempty"`
	Content     string    `json:"content,omitempty"`
}

func (req RuntimeProfileWriteRequest) input() (runtimeprofiles.ProfileInput, error) {
	if req.Content == "" {
		in := runtimeprofiles.ProfileInput{Name: req.Name, Description: req.Description, Presets: req.Presets}
		if req.Spec != nil {
			in.Spec = *req.Spec
		}
		return in, nil
	}
	if req.Name != "" || req.Description != "" || req.Spec != nil || len(req.Presets) > 0 {
		return runtimeprofiles.ProfileInput{}, runtimeBodyError("content excludes name, description, spec and presets; send one or the other")
	}
	var in runtimeprofiles.ProfileInput
	if err := decodeRuntimeContent(req.Content, &in); err != nil {
		return runtimeprofiles.ProfileInput{}, err
	}
	return in, nil
}

type RuntimeProfileResolveFlags struct{}

func (RuntimeProfileResolveFlags) ClickyActionFlags() {}

func registerRuntimeProfileEntity() {
	clicky.NewEntity[RuntimeProfileRecord, RuntimeProfileListOptions, RuntimeProfileRecord]("runtime-profile").
		Aliases("runtime-profiles").
		ToolGroup(runtimeToolGroup).
		ListWithContext(listRuntimeProfiles).
		GetWithContext(getRuntimeProfile).
		CreateWithContext(createRuntimeProfile).
		UpdateWithContext(updateRuntimeProfile).
		DeleteWithContext(deleteRuntimeProfile).
		WithAction(clicky.ActionWithFlagsAndContext("resolve", RuntimeProfileResolveFlags{}, resolveRuntimeProfileAction).
			WithShort("Resolve a profile through its presets: the effective spec with its layer trace").
			WithMethod(http.MethodGet)).
		Register()
}

func listRuntimeProfiles(ctx context.Context, opts RuntimeProfileListOptions) ([]RuntimeProfileRecord, error) {
	catalog, err := buildRuntimeCatalog(ctx, runtimeprofiles.DefaultCatalogOptions{})
	if err != nil {
		return nil, err
	}
	profiles, err := runtimeProfileCandidates(ctx, catalog, opts.Preset)
	if err != nil {
		return nil, runtimeCatalogError(err)
	}
	out := []RuntimeProfileRecord{}
	for _, profile := range profiles {
		if !runtimeSourceMatches(profile.Source, opts.Source) || !runtimeQueryMatches(opts.Query, profile.Name, profile.Description) {
			continue
		}
		out = append(out, RuntimeProfileRecord{Profile: profile})
	}
	sortRuntimeRecords(out, func(record RuntimeProfileRecord) (runtimeprofiles.SourceKind, string) {
		return record.Source.Kind, record.Name
	})
	return out, nil
}

// runtimeProfileCandidates narrows the listing to the profiles referencing one
// preset (by id or name) when a preset filter is given.
func runtimeProfileCandidates(ctx context.Context, catalog *runtimeprofiles.Catalog, presetRef string) ([]runtimeprofiles.Profile, error) {
	if strings.TrimSpace(presetRef) == "" {
		return catalog.ListProfiles(ctx)
	}
	preset, err := catalog.GetPreset(ctx, presetRef)
	if err != nil {
		return nil, err
	}
	return catalog.ReferencedBy(ctx, preset)
}

func getRuntimeProfile(ctx context.Context, id string) (RuntimeProfileRecord, error) {
	catalog, err := buildRuntimeCatalog(ctx, runtimeprofiles.DefaultCatalogOptions{})
	if err != nil {
		return RuntimeProfileRecord{}, err
	}
	profile, err := catalog.GetProfile(ctx, id)
	if err != nil {
		return RuntimeProfileRecord{}, runtimeCatalogError(err)
	}
	return RuntimeProfileRecord{Profile: profile}, nil
}

func createRuntimeProfile(ctx context.Context, body map[string]any) (RuntimeProfileRecord, error) {
	var req RuntimeProfileWriteRequest
	if err := decodeRuntimeBody(ctx, body, &req); err != nil {
		return RuntimeProfileRecord{}, err
	}
	if err := requireNoRuntimeID(req.ID); err != nil {
		return RuntimeProfileRecord{}, err
	}
	in, err := req.input()
	if err != nil {
		return RuntimeProfileRecord{}, err
	}
	catalog, err := buildRuntimeCatalog(ctx, runtimeprofiles.DefaultCatalogOptions{})
	if err != nil {
		return RuntimeProfileRecord{}, err
	}
	if err := requireRuntimeTarget(catalog, req.Target); err != nil {
		return RuntimeProfileRecord{}, err
	}
	profile, err := catalog.CreateProfile(ctx, req.Target, in)
	if err != nil {
		return RuntimeProfileRecord{}, runtimeCatalogError(err)
	}
	return RuntimeProfileRecord{Profile: profile}, nil
}

func updateRuntimeProfile(ctx context.Context, id string, body map[string]any) (RuntimeProfileRecord, error) {
	var req RuntimeProfileWriteRequest
	if err := decodeRuntimeBody(ctx, body, &req); err != nil {
		return RuntimeProfileRecord{}, err
	}
	if err := requireRuntimeIDMatch(req.ID, id); err != nil {
		return RuntimeProfileRecord{}, err
	}
	in, err := req.input()
	if err != nil {
		return RuntimeProfileRecord{}, err
	}
	catalog, err := buildRuntimeCatalog(ctx, runtimeprofiles.DefaultCatalogOptions{})
	if err != nil {
		return RuntimeProfileRecord{}, err
	}
	profile, err := catalog.UpdateProfile(ctx, id, in)
	if err != nil {
		return RuntimeProfileRecord{}, runtimeCatalogError(err)
	}
	return RuntimeProfileRecord{Profile: profile}, nil
}

func deleteRuntimeProfile(ctx context.Context, id string) error {
	catalog, err := buildRuntimeCatalog(ctx, runtimeprofiles.DefaultCatalogOptions{})
	if err != nil {
		return err
	}
	return runtimeCatalogError(catalog.DeleteProfile(ctx, id))
}

// resolveRuntimeProfileAction serves `captain runtime-profile resolve <id>` and
// GET /api/v1/runtime-profile/{id}/resolve: the profile with its references
// canonicalised, the presets in reference order, and the resolved spec.
func resolveRuntimeProfileAction(ctx context.Context, id string, _ map[string]string) (runtimeprofiles.Resolution, error) {
	catalog, err := buildRuntimeCatalog(ctx, runtimeprofiles.DefaultCatalogOptions{})
	if err != nil {
		return runtimeprofiles.Resolution{}, err
	}
	resolution, err := catalog.Resolve(ctx, id)
	if err != nil {
		return runtimeprofiles.Resolution{}, runtimeCatalogError(err)
	}
	return resolution, nil
}
