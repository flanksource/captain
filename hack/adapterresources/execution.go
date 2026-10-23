package main

import (
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/adapters/presentation"
)

func annotateExecutionSchemas() {
	paths, err := filepath.Glob("pkg/adapters/*/*.schema.json")
	must(err)
	for _, path := range paths {
		schema := readObject(path)
		provider := schema["x-captain-adapter"].(map[string]any)["provider"].(string)
		mode := schema["x-captain-adapter"].(map[string]any)["mode"].(string)
		schema["title"] = presentation.Title(provider) + " " + presentation.Title(mode) + " options"
		docs := map[string]string{
			"anthropic": "https://code.claude.com/docs/en/settings-reference",
			"openai":    "https://learn.chatgpt.com/docs/config-file/config-reference",
			"google":    "https://geminicli.com/docs/reference/configuration/",
			"deepseek":  "https://api-docs.deepseek.com/api/create-chat-completion/",
			"jev":       "https://github.com/flanksource/captain",
		}[provider]
		presentation.AnnotateSchema(schema, docs)
		markNativeUnions(schema)
		if strings.HasSuffix(path, "cli.schema.json") {
			versions := map[string]string{"anthropic": "2.1.309", "openai": "codex-cli 0.164.0", "google": "0.64.0"}
			for _, raw := range schema["x-captain-adapter"].(map[string]any)["sources"].([]any) {
				source := raw.(map[string]any)
				if source["kind"] == "cli-help" {
					source["version"] = versions[provider]
				}
			}
		}
		writeObject(path, schema)
	}
}
