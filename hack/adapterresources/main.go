// adapterresources generates adapter schemas from captured upstream sources.
//
//	resources  native provider resource schemas + the published catalog (CI, schemas branch)
//	execution  enriches the committed execution schemas (needs locally captured CLI help/protocols)
package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/adapters"
	"github.com/flanksource/captain/pkg/adapters/presentation"
)

const usage = "usage: adapterresources resources [-sources DIR] [-out DIR] | execution [-sources DIR] [-resources DIR]"

type nativeSource struct{ provider, file, version, reference, docs, patches string }

var nativeSources = []nativeSource{
	{"openai", "codex-config.json", "codex-cli 0.164.0", "https://raw.githubusercontent.com/openai/codex/rust-v0.164.0/codex-rs/core/config.schema.json", "https://learn.chatgpt.com/docs/config-file/config-reference", ""},
	{"anthropic", "claude-settings.json", "@anthropic-ai/claude-agent-sdk 0.3.280; ts-json-schema-generator 2.9.0", "@anthropic-ai/claude-agent-sdk/sdk.d.ts#Settings", "https://code.claude.com/docs/en/settings-reference", "anthropic.yaml"},
	{"google", "gemini-settings.json", "gemini-cli 0.64.0", "https://raw.githubusercontent.com/google-gemini/gemini-cli/v0.64.0/schemas/settings.schema.json", "https://geminicli.com/docs/reference/configuration/", ""},
}

var sourcesDir string

func main() {
	if len(os.Args) < 2 {
		panic(usage)
	}
	flags := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	flags.StringVar(&sourcesDir, "sources", ".tmp/adapter-schema-sources", "captured upstream sources (hack/adapterresources/fetch-sources.sh)")
	out := flags.String("out", ".tmp/adapter-schemas", "directory for the generated native resource schemas and catalog")
	resources := flags.String("resources", ".tmp/adapter-schemas", "directory holding the generated native resource schemas")
	must(flags.Parse(os.Args[2:]))
	switch os.Args[1] {
	case "resources":
		writeNativeResources(*out)
	case "execution":
		enrichRuntimeSchemas(*resources)
		enrichAPISchemas()
		annotateExecutionSchemas()
		inventoryCLIOptions()
	default:
		panic(usage)
	}
}

func writeNativeResources(out string) {
	must(os.MkdirAll(out, 0o755))
	sources := map[string]map[string]any{}
	for _, source := range nativeSources {
		schema := nativeResource(source)
		writeObject(filepath.Join(out, source.provider+".json"), schema)
		sources[source.provider] = schema
	}
	documents, err := adapters.NativeResourceSchemas(sources)
	must(err)
	encoded, err := json.Marshal(documents)
	must(err)
	must(os.WriteFile(filepath.Join(out, adapters.NativeResourceCatalogFile), append(encoded, '\n'), 0o644))
	fmt.Printf("%s: %d documents\n", adapters.NativeResourceCatalogFile, len(documents))
}

func nativeResource(source nativeSource) map[string]any {
	raw, err := os.ReadFile(sourcePath(source.file))
	must(err)
	schema := applyPatches(source.patches, raw)
	flattenRoot(schema)
	if source.provider == "openai" {
		completePermissionMaps(schema)
	}
	if source.provider == "anthropic" {
		mergeClaudeResources(schema)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || len(properties) < 20 {
		panic(fmt.Sprintf("%s has no complete native configuration: %d properties", source.provider, len(properties)))
	}
	schema["title"] = strings.ToUpper(source.provider[:1]) + source.provider[1:] + " configuration"
	schema["description"] = "Complete native " + source.provider + " configuration captured from " + source.version + "."
	schema["x-captain-resource"] = map[string]any{
		"provider": source.provider, "version": source.version, "reference": source.reference,
		"sourceSha256": fmt.Sprintf("%x", sha256.Sum256(raw)), "propertyCount": len(properties),
	}
	if source.patches != "" {
		schema["x-captain-resource"].(map[string]any)["patches"] = "hack/adapterresources/patches/" + source.patches
	}
	schema["x-captain-generated"] = "go run ./hack/adapterresources resources"
	presentation.AnnotateSchema(schema, source.docs)
	fmt.Printf("%s: %d top-level properties\n", source.provider, len(properties))
	return schema
}

func sourcePath(name string) string { return filepath.Join(sourcesDir, name) }

func mergeClaudeResources(schema map[string]any) {
	definitions := schema["definitions"].(map[string]any)
	for _, resource := range []string{"mcp", "agent", "plugin"} {
		raw, err := os.ReadFile(sourcePath("claude-" + resource + ".json"))
		must(err)
		var extra map[string]any
		must(json.Unmarshal(raw, &extra))
		for key, value := range extra["definitions"].(map[string]any) {
			if existing, ok := definitions[key]; ok {
				left, err := json.Marshal(existing)
				must(err)
				right, err := json.Marshal(value)
				must(err)
				if string(left) != string(right) {
					panic("conflicting native definition " + key)
				}
			}
			definitions[key] = value
		}
	}
}

func flattenRoot(schema map[string]any) {
	for schema["$ref"] != nil {
		ref, ok := schema["$ref"].(string)
		if !ok || !strings.HasPrefix(ref, "#/definitions/") {
			panic(fmt.Sprintf("unexpected root reference %v", schema["$ref"]))
		}
		definitions := schema["definitions"].(map[string]any)
		target := definitions[strings.TrimPrefix(ref, "#/definitions/")].(map[string]any)
		delete(schema, "$ref")
		for key, value := range target {
			schema[key] = value
		}
	}
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
