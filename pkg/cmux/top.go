package cmux

import (
	"encoding/json"
	"fmt"
	"sort"
)

type ProcessResources struct {
	PIDs []int `json:"pids"`
}

type TopSnapshot struct {
	Windows []TopWindow `json:"windows"`
}

type TopWindow struct {
	Workspaces []TopWorkspace `json:"workspaces"`
}

type TopWorkspace struct {
	ID        string           `json:"id"`
	Ref       string           `json:"ref"`
	Title     string           `json:"title"`
	Panes     []TopPane        `json:"panes"`
	Resources ProcessResources `json:"resources"`
}

type TopPane struct {
	ID        string           `json:"id"`
	Ref       string           `json:"ref"`
	Surfaces  []TopSurface     `json:"surfaces"`
	Resources ProcessResources `json:"resources"`
}

type TopSurface struct {
	ID        string           `json:"id"`
	Ref       string           `json:"ref"`
	Title     string           `json:"title"`
	TTY       string           `json:"tty"`
	Resources ProcessResources `json:"resources"`
}

type Selector struct {
	WorkspaceID  string `json:"workspace_id,omitempty"`
	WorkspaceRef string `json:"workspace_ref,omitempty"`
	PaneID       string `json:"pane_id,omitempty"`
	PaneRef      string `json:"pane_ref,omitempty"`
	SurfaceID    string `json:"surface_id,omitempty"`
	SurfaceRef   string `json:"surface_ref,omitempty"`
}

type ProcessLocation struct {
	WorkspaceID    string `json:"workspace_id"`
	WorkspaceRef   string `json:"workspace_ref"`
	WorkspaceTitle string `json:"workspace_title,omitempty"`
	PaneID         string `json:"pane_id"`
	PaneRef        string `json:"pane_ref"`
	SurfaceID      string `json:"surface_id"`
	SurfaceRef     string `json:"surface_ref"`
	SurfaceTitle   string `json:"surface_title,omitempty"`
	TTY            string `json:"tty,omitempty"`
}

type ResolvedTarget struct {
	Kind      string                    `json:"kind"`
	Selector  Selector                  `json:"selector"`
	PIDs      []int                     `json:"pids"`
	Locations map[int][]ProcessLocation `json:"-"`
}

func Top() (TopSnapshot, error) {
	out, err := run("--json", "--id-format", "both", "top", "--all")
	if err != nil {
		return TopSnapshot{}, err
	}

	var snapshot TopSnapshot
	if err := json.Unmarshal([]byte(out), &snapshot); err != nil {
		return TopSnapshot{}, fmt.Errorf("parse cmux top output: %w", err)
	}
	return snapshot, nil
}

func (s TopSnapshot) Resolve(selector Selector) (ResolvedTarget, error) {
	kind := selector.Kind()
	if kind == "" {
		return ResolvedTarget{}, fmt.Errorf("cmux selector is empty")
	}

	var matches []resolvedScope
	for _, window := range s.Windows {
		for _, workspace := range window.Workspaces {
			if !matchesIdentity(workspace.ID, workspace.Ref, selector.WorkspaceID, selector.WorkspaceRef) {
				continue
			}
			matches = append(matches, matchingScopes(workspace, selector, kind)...)
		}
	}
	if len(matches) == 0 {
		return ResolvedTarget{}, fmt.Errorf("selector does not identify a cmux target")
	}
	if len(matches) > 1 {
		return ResolvedTarget{}, fmt.Errorf("selector identifies %d cmux targets", len(matches))
	}
	return matches[0].target(selector, kind), nil
}

type locatedSurface struct {
	workspace TopWorkspace
	pane      TopPane
	surface   TopSurface
}

type resolvedScope struct {
	surfaces []locatedSurface
}

func (selector Selector) Kind() string {
	switch {
	case selector.SurfaceID != "" || selector.SurfaceRef != "":
		return "surface"
	case selector.PaneID != "" || selector.PaneRef != "":
		return "pane"
	case selector.WorkspaceID != "" || selector.WorkspaceRef != "":
		return "workspace"
	default:
		return ""
	}
}

func matchingScopes(workspace TopWorkspace, selector Selector, kind string) []resolvedScope {
	if kind == "workspace" {
		return []resolvedScope{{surfaces: workspaceSurfaces(workspace)}}
	}

	var scopes []resolvedScope
	for _, pane := range workspace.Panes {
		if !matchesIdentity(pane.ID, pane.Ref, selector.PaneID, selector.PaneRef) {
			continue
		}
		if kind == "pane" {
			scopes = append(scopes, resolvedScope{surfaces: paneSurfaces(workspace, pane)})
			continue
		}
		for _, surface := range pane.Surfaces {
			if matchesIdentity(surface.ID, surface.Ref, selector.SurfaceID, selector.SurfaceRef) {
				scopes = append(scopes, resolvedScope{surfaces: []locatedSurface{{workspace, pane, surface}}})
			}
		}
	}
	return scopes
}

func matchesIdentity(id, ref, requestedID, requestedRef string) bool {
	return (requestedID == "" || requestedID == id) && (requestedRef == "" || requestedRef == ref)
}

func workspaceSurfaces(workspace TopWorkspace) []locatedSurface {
	var surfaces []locatedSurface
	for _, pane := range workspace.Panes {
		surfaces = append(surfaces, paneSurfaces(workspace, pane)...)
	}
	return surfaces
}

func paneSurfaces(workspace TopWorkspace, pane TopPane) []locatedSurface {
	surfaces := make([]locatedSurface, 0, len(pane.Surfaces))
	for _, surface := range pane.Surfaces {
		surfaces = append(surfaces, locatedSurface{workspace, pane, surface})
	}
	return surfaces
}

func (scope resolvedScope) target(selector Selector, kind string) ResolvedTarget {
	locations := make(map[int][]ProcessLocation)
	for _, item := range scope.surfaces {
		location := ProcessLocation{
			WorkspaceID: item.workspace.ID, WorkspaceRef: item.workspace.Ref, WorkspaceTitle: stripStatusGlyph(item.workspace.Title),
			PaneID: item.pane.ID, PaneRef: item.pane.Ref,
			SurfaceID: item.surface.ID, SurfaceRef: item.surface.Ref, SurfaceTitle: stripStatusGlyph(item.surface.Title), TTY: item.surface.TTY,
		}
		for _, pid := range item.surface.Resources.PIDs {
			locations[pid] = append(locations[pid], location)
		}
	}
	pids := make([]int, 0, len(locations))
	for pid := range locations {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	return ResolvedTarget{Kind: kind, Selector: selector, PIDs: pids, Locations: locations}
}
