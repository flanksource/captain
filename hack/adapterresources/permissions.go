package main

// Codex's flattened Rust maps are not emitted by schemars. These value types
// come from codex-rs/config/src/permissions_toml.rs at rust-v0.164.0.
func completePermissionMaps(schema map[string]any) {
	definitions := schema["definitions"].(map[string]any)
	for name, target := range map[string]string{
		"PermissionsToml":                  "PermissionProfileToml",
		"FilesystemPermissionsToml":        "FilesystemPermissionToml",
		"NetworkDomainPermissionsToml":     "NetworkDomainPermissionToml",
		"NetworkUnixSocketPermissionsToml": "NetworkUnixSocketPermissionToml",
		"WorkspaceRootsToml":               "",
	} {
		value := map[string]any{"type": "boolean"}
		if target != "" {
			value = map[string]any{"$ref": "#/definitions/" + target}
		}
		node := definitions[name].(map[string]any)
		node["additionalProperties"] = value
		node["x-captain-source"] = "https://github.com/openai/codex/blob/rust-v0.164.0/codex-rs/config/src/permissions_toml.rs"
	}
}
