package api

import (
	"strings"

	clickyentity "github.com/flanksource/clicky/entity"
)

// legacyCatalogOn is what "on" means in tool metadata published by an MCP server
// or a clicky operation catalog. Like spec.toolPreferences and unlike the legacy
// permissions.tools shape, that encoding has no separate allow list, so "on" is
// its only way to say auto-run. See ParseToolPolicyOptions.LegacyOn.
const legacyCatalogOn = ToolPolicyAllow

// ToolInfo is the concrete tool being considered for approval and preference
// resolution.
type ToolInfo struct {
	Name string
	// Group is the tool-group this tool belongs to. When non-empty the
	// preferences UI presents the group as one entry governing every member.
	Group             string
	Parent            string
	Icon              string
	DefaultPermission ToolPolicy
	Strict            *bool
	ReadOnlyHint      *bool
	DestructiveHint   *bool
	IdempotentHint    *bool
	// Operation is the clicky RPC operation this tool projects, when it projects
	// one. It is carried whole rather than flattened into strings: clicky already
	// models entity, verb, scope and method as typed fields, and a string copy of
	// each has to be kept in step by hand on both sides of the boundary. Nil for
	// tools that are not clicky operations (MCP servers, app-owned caller tools).
	Operation *clickyentity.RPCOperation
	// Annotations carries opaque caller metadata for policies that want raw
	// values. The clicky operation's own facts travel in Operation, not here.
	Annotations map[string]string
}

// Annotation returns the named annotation (empty when absent).
func (i ToolInfo) Annotation(key string) string {
	if i.Annotations == nil {
		return ""
	}
	return i.Annotations[key]
}

// The accessors below read the operation's identity so no consumer has to
// dereference Operation or its Clicky metadata itself; each is empty for a tool
// that projects no clicky operation.

// Method is the HTTP method the operation is served with, upper-cased.
func (i ToolInfo) Method() string {
	if i.Operation == nil {
		return ""
	}
	return strings.ToUpper(i.Operation.Method)
}

// Path is the REST path the operation is served at.
func (i ToolInfo) Path() string {
	if i.Operation == nil {
		return ""
	}
	return i.Operation.Path
}

// OperationName is the operation's registered name.
func (i ToolInfo) OperationName() string {
	if i.Operation == nil {
		return ""
	}
	return i.Operation.Name
}

// Verb is the entity verb (list/get/create/update/delete, or a custom action).
func (i ToolInfo) Verb() string {
	if i.Operation == nil || i.Operation.Clicky == nil {
		return ""
	}
	return i.Operation.Clicky.Verb
}

// Scope reports whether the operation addresses a collection or a single entity.
func (i ToolInfo) Scope() string {
	if i.Operation == nil || i.Operation.Clicky == nil {
		return ""
	}
	return i.Operation.Clicky.Scope
}

// Entity is the entity the operation belongs to.
func (i ToolInfo) Entity() string {
	if i.Operation == nil || i.Operation.Clicky == nil {
		return ""
	}
	return i.Operation.Clicky.Entity
}

// Action is the custom action's name, empty for a standard verb.
func (i ToolInfo) Action() string {
	if i.Operation == nil || i.Operation.Clicky == nil {
		return ""
	}
	return i.Operation.Clicky.ActionName
}

// ToolCatalog groups the frontend-facing metadata for available tools.
type ToolCatalog struct {
	Tools []ToolCatalogEntry `json:"tools"`
}

// ToolCatalogEntry is the frontend-facing DTO for one tool. Unlike ToolInfo it
// keeps method/path/operationName as typed fields, since the tool-preferences UI
// renders them.
type ToolCatalogEntry struct {
	Name              string         `json:"name"`
	Title             string         `json:"title,omitempty"`
	Description       string         `json:"description,omitempty"`
	Source            string         `json:"source"`
	Server            string         `json:"server,omitempty"`
	Group             string         `json:"group,omitempty"`
	Parent            string         `json:"parent,omitempty"`
	Icon              string         `json:"icon,omitempty"`
	PreferenceKey     string         `json:"preferenceKey,omitempty"`
	DefaultPermission ToolPolicy     `json:"defaultPermission,omitempty"`
	Strict            *bool          `json:"strict,omitempty"`
	Method            string         `json:"method,omitempty"`
	Path              string         `json:"path,omitempty"`
	OperationName     string         `json:"operationName,omitempty"`
	InputSchema       map[string]any `json:"inputSchema"`
	OutputSchema      map[string]any `json:"outputSchema,omitempty"`
}

// DefaultToolPolicy resolves a tool's declared policy, defaulting an unset or
// unrecognised value to auto so safety hints or the runtime policy decide.
func DefaultToolPolicy(policy ToolPolicy) ToolPolicy {
	if normalized, ok := NormalizeToolPolicy(string(policy)); ok {
		return normalized
	}
	return ToolPolicyAuto
}

// CustomCatalogEntry builds the catalog DTO for an app-owned custom tool. The
// method/path/operationName it surfaces are read from the clicky operation the
// tool projects, and are empty for a tool that projects none.
func CustomCatalogEntry(def ToolDefinition, name string, schema map[string]any) ToolCatalogEntry {
	policy := DefaultToolPolicy(def.DefaultPermission)
	info := ToolInfo{
		Name:              name,
		Group:             def.Group,
		Parent:            def.Parent,
		Icon:              def.Icon,
		DefaultPermission: policy,
		Strict:            def.Strict,
		Operation:         def.Operation,
		Annotations:       def.Annotations,
	}
	return ToolCatalogEntry{
		Name:              name,
		Title:             def.Name,
		Description:       def.Description,
		Source:            "custom",
		Group:             def.Group,
		Parent:            def.Parent,
		Icon:              def.Icon,
		PreferenceKey:     PreferenceKey(info),
		DefaultPermission: policy,
		Strict:            def.Strict,
		Method:            info.Method(),
		Path:              info.Path(),
		OperationName:     def.Name,
		InputSchema:       ObjectSchema(schema),
	}
}

// PreferenceKey returns the key a tool is governed by in the preferences UI: its
// group when grouped, otherwise its own name.
func PreferenceKey(info ToolInfo) string {
	if info.Group != "" {
		return info.Group
	}
	return info.Name
}

// ObjectSchema defaults a nil/typeless schema to an empty JSON object schema.
func ObjectSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	if _, ok := schema["type"]; !ok {
		schema["type"] = "object"
	}
	if schema["type"] == "object" {
		if _, ok := schema["properties"]; !ok {
			schema["properties"] = map[string]any{}
		}
	}
	return schema
}

// StringMetadata returns the first non-empty string value among keys.
func StringMetadata(meta map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		if v, ok := meta[key].(string); ok && v != "" {
			return v, true
		}
	}
	return "", false
}

// ApplyToolMetadata overlays MCP tool metadata (group/parent/icon/permission/
// strict, incl. the nested com.flanksource.clicky/tool block) onto an entry.
//
// The permission is parsed with the legacy on/off spellings accepted, because
// the publishers on the other side of this wire still emit them —
// clicky/entity.ToolPermission is on|off|ask|auto, and clicky/mcp/registry
// copies it verbatim into _meta.defaultPermission. Rejecting them here would
// silently fall back to auto, and a server that asked for "off" would have its
// tool exposed rather than omitted.
func ApplyToolMetadata(entry *ToolCatalogEntry, meta map[string]any) {
	if entry == nil || len(meta) == 0 {
		return
	}
	if group, ok := StringMetadata(meta, "group", "toolGroup"); ok && entry.Group == "" {
		entry.Group = group
		entry.PreferenceKey = group
	}
	if parent, ok := StringMetadata(meta, "parent", "toolParent"); ok && entry.Parent == "" {
		entry.Parent = parent
	}
	if icon, ok := StringMetadata(meta, "icon", "toolIcon"); ok && entry.Icon == "" {
		entry.Icon = icon
	}
	if permission, ok := StringMetadata(meta, "defaultPermission", "permission", "defaultMode"); ok {
		policy, parsed := ParseToolPolicy(permission, ParseToolPolicyOptions{LegacyOn: legacyCatalogOn})
		if !parsed {
			policy = ToolPolicyAuto
		}
		entry.DefaultPermission = policy
	}
	if strict, ok := BoolMetadata(meta, "strict"); ok && entry.Strict == nil {
		entry.Strict = &strict
	}
	if nested, ok := meta["com.flanksource.clicky/tool"].(map[string]any); ok {
		ApplyToolMetadata(entry, nested)
	}
}

// BoolMetadata returns the first bool-valued (or "true"/"false" string) key.
func BoolMetadata(meta map[string]any, keys ...string) (bool, bool) {
	for _, key := range keys {
		switch v := meta[key].(type) {
		case bool:
			return v, true
		case string:
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "true":
				return true, true
			case "false":
				return false, true
			}
		}
	}
	return false, false
}
