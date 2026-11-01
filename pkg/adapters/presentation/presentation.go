package presentation

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// AnnotateSchema adds form presentation without changing validation keywords.
func AnnotateSchema(schema map[string]any, docs string) {
	annotateNode(schema, "Configuration", docs)
}

func annotateNode(node map[string]any, name, docs string) {
	annotateArbitraryJSON(node)
	if node["title"] == nil {
		node["title"] = Title(name)
	}
	if node["description"] == nil && name != "Configuration" {
		node["description"] = fmt.Sprintf("%s configuration. See the linked native reference for semantics and availability.", Title(name))
	}
	if docs != "" && node["x-help"] == nil {
		node["x-help"] = map[string]any{"links": []any{map[string]any{"label": "Documentation", "href": docs}}}
	}
	annotateEnum(node)
	if properties, ok := node["properties"].(map[string]any); ok {
		keys := sortedKeys(properties)
		for i, key := range keys {
			if child, ok := properties[key].(map[string]any); ok {
				if child["x-clicky-order"] == nil {
					child["x-clicky-order"] = (i + 1) * 10
				}
				annotateNode(child, key, docs)
			}
		}
	}
	for _, keyword := range []string{"$defs", "definitions", "patternProperties"} {
		if children, ok := node[keyword].(map[string]any); ok {
			for _, key := range sortedKeys(children) {
				if child, ok := children[key].(map[string]any); ok {
					annotateNode(child, key, docs)
				}
			}
		}
	}
	for _, keyword := range []string{"items", "additionalProperties", "propertyNames", "if", "then", "else", "not"} {
		if child, ok := node[keyword].(map[string]any); ok {
			annotateNode(child, name+" value", docs)
		}
	}
	for _, keyword := range []string{"oneOf", "anyOf", "allOf", "prefixItems"} {
		if children, ok := node[keyword].([]any); ok {
			for _, raw := range children {
				if child, ok := raw.(map[string]any); ok {
					annotateNode(child, name, docs)
				}
			}
		}
	}
	if node["type"] == "array" {
		items, _ := node["items"].(map[string]any)
		plainString := items["type"] == "string"
		for _, keyword := range []string{"$ref", "enum", "const", "oneOf", "anyOf", "allOf", "properties", "items", "additionalProperties"} {
			if items[keyword] != nil {
				plainString = false
			}
		}
		if !plainString && node["x-array-display"] == "list" {
			delete(node, "x-array-display")
		}
		if plainString && node["x-array-display"] == nil {
			node["x-array-display"] = "list"
		}
	}
}

func annotateArbitraryJSON(node map[string]any) {
	if node["type"] == "object" {
		_, properties := node["properties"]
		if node["additionalProperties"] == true || (!properties && node["additionalProperties"] == nil) {
			node["additionalProperties"] = map[string]any{"format": "json"}
		}
		return
	}
	if node["format"] != nil {
		return
	}
	for _, keyword := range []string{"type", "$ref", "enum", "const", "properties", "additionalProperties", "patternProperties", "items", "anyOf", "oneOf", "allOf", "not", "if", "required"} {
		if _, exists := node[keyword]; exists {
			return
		}
	}
	node["format"] = "json"
}

func annotateEnum(node map[string]any) {
	values, ok := node["enum"].([]any)
	if !ok || len(values) == 0 {
		return
	}
	labels := map[string]any{}
	existing, _ := node["x-enum-labels"].(map[string]any)
	icons := map[string]any{}
	for _, raw := range values {
		value, ok := raw.(string)
		if !ok {
			return
		}
		labels[value] = Title(value)
		if value == "" {
			labels[value] = "Default"
		}
		if label, ok := existing[value].(string); ok && label != "" {
			labels[value] = label
		}
		if icon, ok := enumIcons[value]; ok {
			icons[value] = icon
		}
	}
	node["x-enum-labels"] = labels
	if node["x-enum-display"] == nil {
		node["x-enum-display"] = "combobox"
		if len(values) <= 4 {
			node["x-enum-display"] = "segmented"
		}
	}
	if len(icons) == len(values) && node["x-enum-icons"] == nil {
		node["x-enum-icons"] = icons
	}
}

var enumIcons = map[string]string{
	"":          "shield",
	"read-only": "lock", "read": "lock", "workspace-write": "folder", "write": "folder",
	"danger-full-access": "lock-open", "full": "lock-open", "deny": "prohibit", "allow": "check",
	"ask": "question", "auto": "sparkles", "default": "shield", "plan": "list-dashes",
	"acceptEdits": "folder", "bypassPermissions": "lock-open", "dontAsk": "prohibit",
	"enabled": "check", "disabled": "prohibit", "restricted": "shield", "unrestricted": "globe",
	"off": "prohibit", "native": "shield", "docker": "package", "git-agent": "git-branch",
}

// Title turns a native property name into a form label.
func Title(value string) string {
	var out []rune
	var previous rune
	for _, r := range value {
		if r == '_' || r == '-' {
			out = append(out, ' ')
			previous = r
			continue
		}
		if unicode.IsUpper(r) && unicode.IsLower(previous) {
			out = append(out, ' ')
		}
		if len(out) == 0 {
			r = unicode.ToUpper(r)
		}
		out = append(out, r)
		previous = r
	}
	words := strings.Fields(string(out))
	for index, word := range words {
		if replacement, ok := map[string]string{"openai": "OpenAI", "deepseek": "DeepSeek", "mcp": "MCP", "api": "API", "cli": "CLI", "http": "HTTP", "https": "HTTPS", "json": "JSON", "yaml": "YAML", "toml": "TOML", "sdk": "SDK"}[strings.ToLower(word)]; ok {
			words[index] = replacement
		}
	}
	return strings.Join(words, " ")
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
