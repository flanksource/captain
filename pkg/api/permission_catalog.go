package api

// PermissionCatalog lists permission targets available to the current workspace.
type PermissionCatalog struct {
	Tools   []PermissionCatalogItem `json:"tools"`
	MCP     []PermissionCatalogItem `json:"mcp"`
	Plugins []PermissionCatalogItem `json:"plugins"`
	Skills  []PermissionCatalogItem `json:"skills"`
}

// PermissionCatalogItem is one selectable tool, MCP server, plugin, or skill.
type PermissionCatalogItem struct {
	ID          string `json:"id"`
	Label       string `json:"label,omitempty"`
	Group       string `json:"group,omitempty"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source,omitempty"`
	SourcePath  string `json:"sourcePath,omitempty"`
	Configured  bool   `json:"configured,omitempty"`
	Available   bool   `json:"available,omitempty"`
	DefaultMode string `json:"defaultMode,omitempty"`
}
