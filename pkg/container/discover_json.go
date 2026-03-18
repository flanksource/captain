package container

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func discoverSettingsGranular(cfg DiscoverConfig) []Component {
	path := filepath.Join(cfg.ClaudeDir, "settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}

	target := cfg.containerHome() + "/.claude/settings.json"
	var result []Component

	if _, ok := raw["permissions"]; ok {
		result = append(result, Component{
			Category:        CategorySettings,
			Name:            "Permissions (allow all)",
			SourcePath:      path,
			TargetPath:      target,
			ContentKey:      "permissions",
			OptionValue:     "allow-all",
			DefaultSelected: true,
			Description:     "grant all tool permissions",
		})
		result = append(result, Component{
			Category:    CategorySettings,
			Name:        "Permissions (keep)",
			SourcePath:  path,
			TargetPath:  target,
			ContentKey:  "permissions",
			OptionValue: "keep",
			Description: "preserve current permission rules",
		})
	}

	if _, ok := raw["sandbox"]; ok {
		result = append(result, Component{
			Category:        CategorySettings,
			Name:            "Sandbox (off)",
			SourcePath:      path,
			TargetPath:      target,
			ContentKey:      "sandbox",
			OptionValue:     "off",
			DefaultSelected: true,
			Description:     "disable sandbox in container",
		})
		result = append(result, Component{
			Category:    CategorySettings,
			Name:        "Sandbox (keep)",
			SourcePath:  path,
			TargetPath:  target,
			ContentKey:  "sandbox",
			OptionValue: "keep",
			Description: "preserve current sandbox config",
		})
	}

	type section struct {
		name       string
		contentKey string
	}
	others := []section{
		{"Hooks config", "hooks"},
		{"Status line", "statusLine"},
		{"Enabled plugins", "enabledPlugins"},
		{"Preferences", "preferences"},
	}

	preferenceKeys := []string{
		"includeCoAuthoredBy", "effortLevel", "alwaysThinkingEnabled",
		"skipDangerousModePermissionPrompt", "autoUpdatesChannel",
	}

	for _, s := range others {
		if s.contentKey == "preferences" {
			hasAny := false
			for _, pk := range preferenceKeys {
				if _, ok := raw[pk]; ok {
					hasAny = true
					break
				}
			}
			if !hasAny {
				continue
			}
		} else if _, ok := raw[s.contentKey]; !ok {
			continue
		}
		result = append(result, Component{
			Category:   CategorySettings,
			Name:       s.name,
			SourcePath: path,
			TargetPath: target,
			ContentKey: s.contentKey,
		})
	}
	return result
}

func discoverMCPServers(cfg DiscoverConfig) []Component {
	path := ClaudeJSONPath(cfg)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	serversRaw, ok := raw["mcpServers"]
	if !ok {
		return nil
	}
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(serversRaw, &servers); err != nil {
		return nil
	}

	var result []Component
	for name := range servers {
		result = append(result, Component{
			Category:   CategoryMCPServers,
			Name:       name,
			SourcePath: path,
			TargetPath: cfg.containerHome() + "/.claude.json",
			ContentKey: "mcpServers." + name,
		})
	}
	return result
}

func discoverAuth(cfg DiscoverConfig) []Component {
	path := ClaudeJSONPath(cfg)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	if _, ok := raw["oauthAccount"]; !ok {
		return nil
	}
	return []Component{{
		Category:        CategoryAuth,
		Name:            "OAuth account",
		SourcePath:      path,
		TargetPath:      cfg.containerHome() + "/.claude.json",
		ContentKey:      "oauthAccount",
		DefaultSelected: true,
	}}
}

func discoverFeatureFlags(cfg DiscoverConfig) []Component {
	path := ClaudeJSONPath(cfg)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}

	var result []Component
	if _, ok := raw["cachedStatsigGates"]; ok {
		result = append(result, Component{
			Category:   CategoryFeatureFlags,
			Name:       "Statsig gates",
			SourcePath: path,
			TargetPath: cfg.containerHome() + "/.claude.json",
			ContentKey: "cachedStatsigGates",
		})
	}
	if _, ok := raw["cachedGrowthBookFeatures"]; ok {
		result = append(result, Component{
			Category:   CategoryFeatureFlags,
			Name:       "GrowthBook features",
			SourcePath: path,
			TargetPath: cfg.containerHome() + "/.claude.json",
			ContentKey: "cachedGrowthBookFeatures",
		})
	}
	return result
}

func ClaudeJSONPath(cfg DiscoverConfig) string {
	if cfg.ClaudeJSONPath != "" {
		return cfg.ClaudeJSONPath
	}
	if home := os.Getenv("HOME"); home != "" {
		return filepath.Join(home, ".claude.json")
	}
	return filepath.Join(cfg.ClaudeDir, "..", ".claude.json")
}
