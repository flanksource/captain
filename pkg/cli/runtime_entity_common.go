package cli

import (
	"cmp"
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/runtimeprofiles"
	"github.com/flanksource/clicky/entity"
	"gopkg.in/yaml.v3"
)

const runtimeToolGroup = "captain.runtime"

var registerRuntimeEntitiesOnce sync.Once

// RegisterRuntimeEntities publishes the runtime-preset and runtime-profile
// entities on every clicky surface: CLI, /api/v1, OpenAPI and MCP.
func RegisterRuntimeEntities() {
	registerRuntimeEntitiesOnce.Do(func() {
		registerRuntimePresetEntity()
		registerRuntimeProfileEntity()
	})
}

// runtimeSourceMatches accepts an empty filter, a source id, or a source kind
// ("db" or "file").
func runtimeSourceMatches(source runtimeprofiles.SourceInfo, filter string) bool {
	filter = strings.ToLower(strings.TrimSpace(filter))
	return filter == "" || filter == strings.ToLower(source.ID) || filter == string(source.Kind)
}

func runtimeQueryMatches(query string, fields ...string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), query) {
			return true
		}
	}
	return false
}

func runtimeScopeFilter(raw string) (api.SpecLayerScope, error) {
	scope := api.SpecLayerScope(strings.ToLower(strings.TrimSpace(raw)))
	switch scope {
	case "", api.SpecLayerGlobal, api.SpecLayerContext, api.SpecLayerSurface, api.SpecLayerUser:
		return scope, nil
	}
	return "", runtimeBodyError("unknown scope %q; use global, context, surface or user", raw)
}

// sortRuntimeRecords orders database records before file records, then by name
// case-insensitively, so a mixed listing reads the same on every host.
func sortRuntimeRecords[T any](items []T, identity func(T) (runtimeprofiles.SourceKind, string)) {
	slices.SortStableFunc(items, func(left, right T) int {
		leftKind, leftName := identity(left)
		rightKind, rightName := identity(right)
		return cmp.Or(
			cmp.Compare(leftKind, rightKind),
			cmp.Compare(strings.ToLower(leftName), strings.ToLower(rightName)),
		)
	})
}

func runtimeBodyError(format string, args ...any) error {
	return entity.NewStatusErrorf(http.StatusBadRequest, "invalid_body", format, args...)
}

// decodeRuntimeBody reads a create/update body: the raw JSON over HTTP, the
// flat key=value map on the CLI. An unknown field is the caller's mistake.
func decodeRuntimeBody(ctx context.Context, body map[string]any, dst any) error {
	if err := decodePromptBody(ctx, body, dst); err != nil {
		return runtimeBodyError("%v", err)
	}
	return nil
}

// decodeRuntimeContent parses the YAML file form of a record strictly. A YAML
// document cannot start with "@", so that prefix is an unexpanded CLI file
// reference (`content=@x.yaml`), which clicky only expands as `content@x.yaml`.
func decodeRuntimeContent(content string, dst any) error {
	if strings.HasPrefix(strings.TrimSpace(content), "@") {
		return runtimeBodyError("content %q was not read from a file; use content@file.yaml", content)
	}
	decoder := yaml.NewDecoder(strings.NewReader(content))
	decoder.KnownFields(true)
	if err := decoder.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return runtimeBodyError("content is empty")
		}
		return runtimeBodyError("content: %v", err)
	}
	return nil
}

// requireRuntimeIDMatch checks the id the executor routes an update by (it
// travels in the PUT body, since clicky publishes update at the collection
// path) against the id the handler was given.
func requireRuntimeIDMatch(bodyID, id string) error {
	if bodyID != "" && bodyID != id {
		return runtimeBodyError("body id %q does not match %q", bodyID, id)
	}
	return nil
}

func requireNoRuntimeID(bodyID string) error {
	if bodyID != "" {
		return runtimeBodyError("id is assigned by the source; omit it on create")
	}
	return nil
}

// requireRuntimeTarget rejects a create whose target names no registered
// source before the catalog is asked to write anywhere.
func requireRuntimeTarget(catalog *runtimeprofiles.Catalog, target string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil
	}
	for _, source := range catalog.Sources() {
		if source.ID == target {
			return nil
		}
	}
	return runtimeBodyError("unknown target source %q", target)
}

// runtimeModelLabel renders the authored model the way multi-model selectors
// are written (mode:name), or the bare name when no mode is pinned.
func runtimeModelLabel(model api.Model) string {
	if model.Name == "" {
		return ""
	}
	if model.Mode == "" {
		return model.Name
	}
	return string(model.Mode) + ":" + model.Name
}
