package api

import "strings"

// legacyCatalogOn is what "on" means in tool metadata published by an MCP server
// or a clicky operation catalog. Like spec.toolPreferences and unlike the legacy
// permissions.tools shape, that encoding has no separate allow list, so "on" is
// its only way to say auto-run. See ParseToolPolicyOptions.LegacyOn.
const legacyCatalogOn = ToolPolicyAllow

// ToolInfo is the concrete tool being considered for approval and preference
// resolution. Clicky-RPC specifics (verb/method/path/operation) live in
// Annotations, not as typed fields.
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
	// Annotations carries opaque caller metadata (e.g. clicky/verb, clicky/method,
	// clicky/path, clicky/operation) for policies that want the raw values.
	Annotations map[string]string
}

// Annotation returns the named annotation (empty when absent).
func (i ToolInfo) Annotation(key string) string {
	if i.Annotations == nil {
		return ""
	}
	return i.Annotations[key]
}

// ToolCatalog is the GET /api/chat/tools payload.
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
// method/path/operationName it surfaces come from the definition's Annotations
// (clicky/method, clicky/path, clicky/operation) when present.
func CustomCatalogEntry(def ToolDefinition, name string, schema map[string]any) ToolCatalogEntry {
	policy := DefaultToolPolicy(def.DefaultPermission)
	info := ToolInfo{
		Name:              name,
		Group:             def.Group,
		Parent:            def.Parent,
		Icon:              def.Icon,
		DefaultPermission: policy,
		Strict:            def.Strict,
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
		Method:            def.Annotations["clicky/method"],
		Path:              def.Annotations["clicky/path"],
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
