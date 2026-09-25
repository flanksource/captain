package adapters

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

type adapterSchema struct {
	path string
	raw  []byte
	body map[string]any
}

var _ = ginkgo.Describe("Adapter schema contract", func() {
	ginkgo.It("covers every registered native runtime exactly once", func() {
		documents := loadAdapterSchemas()
		actual := make([]string, 0, len(documents))
		for _, document := range documents {
			actual = append(actual, document.path)
		}
		Expect(actual).To(Equal(expectedSchemaPaths()))
	})

	ginkgo.It("compiles every schema with matching provenance", func() {
		for _, document := range loadAdapterSchemas() {
			decoded, err := jsonschema.UnmarshalJSON(bytes.NewReader(document.raw))
			Expect(err).NotTo(HaveOccurred(), document.path)
			compiler := jsonschema.NewCompiler()
			resource := "mem:///captain/adapters/" + document.path
			Expect(compiler.AddResource(resource, decoded)).To(Succeed(), document.path)
			_, err = compiler.Compile(resource)
			Expect(err).NotTo(HaveOccurred(), document.path)

			parts := strings.Split(document.path, string(filepath.Separator))
			Expect(parts).To(HaveLen(2), document.path)
			mode := strings.TrimSuffix(parts[1], ".schema.json")
			Expect(document.body).To(HaveKeyWithValue("$schema", "https://json-schema.org/draft/2020-12/schema"), document.path)
			Expect(document.body).To(HaveKeyWithValue("type", "object"), document.path)
			Expect(document.body).To(HaveKeyWithValue("additionalProperties", false), document.path)

			adapter := objectField(document.body, "x-captain-adapter", document.path)
			Expect(adapter).To(HaveKeyWithValue("provider", parts[0]), document.path)
			Expect(adapter).To(HaveKeyWithValue("mode", mode), document.path)
			assertSources(document.path, mode, adapter["sources"])
		}
	})

	ginkgo.It("embeds every schema for runtime viewers in canonical order", func() {
		documents, err := Schemas()
		Expect(err).NotTo(HaveOccurred())
		Expect(documents).To(HaveLen(len(expectedSchemaPaths())))

		actual := make([]string, 0, len(documents))
		for _, document := range documents {
			path := filepath.Join(document.Provider, document.Mode+".schema.json")
			actual = append(actual, path)
			raw, err := os.ReadFile(path)
			Expect(err).NotTo(HaveOccurred(), path)
			Expect(document.Schema).To(MatchJSON(raw), path)
			Expect(document.Title).NotTo(BeEmpty(), path)
			Expect(document.Description).NotTo(BeEmpty(), path)
		}
		Expect(actual).To(Equal(expectedSchemaCatalogPaths()))
	})

	ginkgo.It("annotates every native option for JsonSchemaForm and Captain support", func() {
		for _, document := range loadAdapterSchemas() {
			walkLeafOptions(document.body, "", func(path string, field map[string]any) {
				label := document.path + "#" + path
				Expect(strings.TrimSpace(stringField(field, "title"))).NotTo(BeEmpty(), label)
				Expect(strings.TrimSpace(stringField(field, "description"))).NotTo(BeEmpty(), label)
				Expect(stringField(field, "x-clicky-section")).To(BeElementOf(
					"model", "prompt", "workspace", "sandbox", "permissions", "environment", "cli",
				), label)
				Expect(field["x-clicky-order"]).To(BeAssignableToTypeOf(float64(0)), label)
				assertOptionSupport(label, objectField(field, "x-captain-option", label), field["readOnly"])
			})
		}
	})

	ginkgo.It("uses globally unique generated type names", func() {
		seen := map[string]string{}
		for _, document := range loadAdapterSchemas() {
			title := stringField(document.body, "title")
			Expect(title).NotTo(BeEmpty(), document.path)
			Expect(seen).NotTo(HaveKey(title), "title %s is reused by %s and %s", title, seen[title], document.path)
			seen[title] = document.path
			for name := range objectFieldOrEmpty(document.body, "$defs") {
				Expect(seen).NotTo(HaveKey(name), "$defs name %s is reused by %s and %s", name, seen[name], document.path)
				seen[name] = document.path
			}
		}
	})

	ginkgo.It("round trips native JSON names through every generated root DTO", func() {
		assertJSONRoundTrip[AnthropicAPIOptions](map[string]any{"model": "claude-sonnet-5", "max_tokens": 2048, "messages": []any{map[string]any{"role": "user"}}, "stream": true, "metadata": map[string]any{"user_id": "tenant-x"}})
		assertJSONRoundTrip[AnthropicAgentOptions](map[string]any{"model": "claude-sonnet-5", "includePartialMessages": true, "agent": "reviewer"})
		assertJSONRoundTrip[AnthropicCLIOptions](map[string]any{"model": "claude-sonnet-5", "print": true, "agent": "reviewer"})
		assertJSONRoundTrip[OpenAIAPIOptions](map[string]any{"model": "gpt-5.6", "input": "hello", "stream": true, "background": true})
		assertJSONRoundTrip[OpenAIAgentOptions](map[string]any{
			"threadStart": map[string]any{
				"model": "gpt-5.6", "cwd": "/workspace", "personality": "friendly",
				"approvalPolicy": "on-request", "permissions": "workspace", "runtimeWorkspaceRoots": []any{"/workspace", "/shared"},
			},
			"threadResume": map[string]any{
				"threadId": "thread-1", "approvalPolicy": "on-request", "permissions": "workspace", "runtimeWorkspaceRoots": []any{"/workspace", "/shared"},
			},
			"turnStart": map[string]any{
				"threadId": "thread-1", "input": []any{map[string]any{"type": "text", "text": "inspect"}},
				"approvalPolicy": "on-request", "permissions": "workspace", "sandboxPolicy": map[string]any{"type": "workspaceWrite", "writableRoots": []any{"/shared"}},
				"collaborationMode": map[string]any{"mode": "plan", "settings": map[string]any{"model": "gpt-5.6", "reasoning_effort": "high", "developer_instructions": nil}},
			},
		})
		assertJSONRoundTrip[OpenAICLIOptions](map[string]any{"model": "gpt-5.6", "json": true, "oss": true})
		assertJSONRoundTrip[GoogleAPIOptions](map[string]any{"model": "gemini-3.5-pro", "contents": []any{map[string]any{"role": "user"}}, "temperature": 0.3, "candidateCount": 1})
		assertJSONRoundTrip[GoogleCLIOptions](map[string]any{"model": "gemini-3.5-pro", "outputFormat": "stream-json", "debug": true})
		assertJSONRoundTrip[DeepSeekAPIOptions](map[string]any{"model": "deepseek-chat", "messages": []any{map[string]any{"role": "user"}}, "stream": true, "frequency_penalty": 0.5})
	})

	ginkgo.It("aligns OpenAI agent mappings with the published runtime schema", func() {
		var document adapterSchema
		for _, candidate := range loadAdapterSchemas() {
			if candidate.path == filepath.Join("openai", "agent.schema.json") {
				document = candidate
				break
			}
		}
		Expect(document.body).NotTo(BeNil())
		runtimeSchema := api.RuntimeSchemaFor(api.OpenAI, api.ModeAgent)
		walkLeafOptions(document.body, "", func(path string, field map[string]any) {
			option := objectField(field, "x-captain-option", path)
			if stringField(option, "support") != "mapped" {
				return
			}
			for _, specPath := range specPaths(option["specPath"]) {
				Expect(runtimeArgumentNames(runtimeSchema, specPath)).To(
					ContainElement(stringField(option, "nativeName")), "%s maps %s", path, specPath,
				)
			}
		})
	})

	ginkgo.It("generates named permission profiles as strings", func() {
		for _, field := range []reflect.StructField{
			generatedField(reflect.TypeFor[ThreadStart](), "Permissions"),
			generatedField(reflect.TypeFor[ThreadResume](), "Permissions"),
			generatedField(reflect.TypeFor[TurnStart](), "Permissions"),
		} {
			Expect(field.Type.Kind()).To(Equal(reflect.Pointer), field.Name)
			Expect(field.Type.Elem().Kind()).To(Equal(reflect.String), field.Name)
		}
	})
})

func loadAdapterSchemas() []adapterSchema {
	root, err := os.Getwd()
	Expect(err).NotTo(HaveOccurred())
	matches, err := filepath.Glob(filepath.Join(root, "*", "*.schema.json"))
	Expect(err).NotTo(HaveOccurred())
	documents := make([]adapterSchema, 0, len(matches))
	for _, match := range matches {
		raw, err := os.ReadFile(match)
		Expect(err).NotTo(HaveOccurred(), match)
		var body map[string]any
		Expect(json.Unmarshal(raw, &body)).To(Succeed(), match)
		path, err := filepath.Rel(root, match)
		Expect(err).NotTo(HaveOccurred(), match)
		documents = append(documents, adapterSchema{path: path, raw: raw, body: body})
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].path < documents[j].path })
	return documents
}

func expectedSchemaPaths() []string {
	paths := expectedSchemaCatalogPaths()
	sort.Strings(paths)
	return paths
}

func expectedSchemaCatalogPaths() []string {
	var paths []string
	for _, provider := range registry.Providers() {
		for _, mode := range provider.Modes() {
			if mode == registry.ModeCmux {
				continue
			}
			paths = append(paths, filepath.Join(provider.Name, string(mode)+".schema.json"))
		}
	}
	return paths
}

func assertSources(path, mode string, raw any) {
	sources, ok := raw.([]any)
	Expect(ok).To(BeTrue(), "%s sources must be an array", path)
	Expect(sources).NotTo(BeEmpty(), path)
	kinds := make([]string, 0, len(sources))
	for index, rawSource := range sources {
		source, ok := rawSource.(map[string]any)
		Expect(ok).To(BeTrue(), "%s source %d", path, index)
		kind := stringField(source, "kind")
		Expect(kind).To(BeElementOf("cli-help", "sdk", "protocol", "official-docs", "implementation"), path)
		Expect(strings.TrimSpace(stringField(source, "reference"))).NotTo(BeEmpty(), path)
		version := strings.TrimSpace(stringField(source, "version"))
		Expect(version).NotTo(BeEmpty(), path)
		Expect(version).NotTo(ContainSubstring("<"), path)
		kinds = append(kinds, kind)
	}
	Expect(kinds).To(ContainElement("implementation"), path)
	switch mode {
	case string(registry.ModeCLI):
		Expect(kinds).To(ContainElement("cli-help"), path)
	case string(registry.ModeAgent):
		Expect(kinds).To(ContainElement(Or(Equal("sdk"), Equal("protocol"))), path)
	case string(registry.ModeAPI):
		Expect(kinds).To(ContainElement(Or(Equal("sdk"), Equal("official-docs"))), path)
	}
}

func walkLeafOptions(schema map[string]any, prefix string, visit func(string, map[string]any)) {
	properties := objectFieldOrEmpty(schema, "properties")
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		field, ok := properties[name].(map[string]any)
		Expect(ok).To(BeTrue(), "%s/%s must be an object", prefix, name)
		path := prefix + "/" + name
		if len(objectFieldOrEmpty(field, "properties")) > 0 {
			walkLeafOptions(field, path, visit)
			continue
		}
		visit(path, field)
	}
}

func assertOptionSupport(label string, option map[string]any, readOnly any) {
	Expect(strings.TrimSpace(stringField(option, "nativeName"))).NotTo(BeEmpty(), label)
	support := stringField(option, "support")
	Expect(support).To(BeElementOf("mapped", "managed", "unsupported"), label)
	specPath, hasSpecPath := option["specPath"]
	note := strings.TrimSpace(stringField(option, "note"))
	switch support {
	case "mapped":
		Expect(hasSpecPath).To(BeTrue(), label)
		Expect(validSpecPath(specPath)).To(BeTrue(), label)
		Expect(readOnly).NotTo(Equal(true), label)
	case "managed", "unsupported":
		Expect(hasSpecPath).To(BeFalse(), label)
		Expect(note).NotTo(BeEmpty(), label)
		Expect(readOnly).To(Equal(true), label)
	}
}

func validSpecPath(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		if len(typed) == 0 {
			return false
		}
		for _, entry := range typed {
			path, ok := entry.(string)
			if !ok || strings.TrimSpace(path) == "" {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func objectField(value map[string]any, name, label string) map[string]any {
	field, ok := value[name].(map[string]any)
	Expect(ok).To(BeTrue(), "%s field %s must be an object", label, name)
	return field
}

func objectFieldOrEmpty(value map[string]any, name string) map[string]any {
	field, _ := value[name].(map[string]any)
	return field
}

func stringField(value map[string]any, name string) string {
	field, _ := value[name].(string)
	return field
}

func assertJSONRoundTrip[T any](input map[string]any) {
	raw, err := json.Marshal(input)
	Expect(err).NotTo(HaveOccurred())
	var dto T
	Expect(json.Unmarshal(raw, &dto)).To(Succeed(), fmt.Sprintf("%T", dto))
	var expected map[string]any
	Expect(json.Unmarshal(raw, &expected)).To(Succeed())
	encoded, err := json.Marshal(dto)
	Expect(err).NotTo(HaveOccurred())
	var actual map[string]any
	Expect(json.Unmarshal(encoded, &actual)).To(Succeed())
	Expect(actual).To(Equal(expected), fmt.Sprintf("%T", dto))
}

func specPaths(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []any:
		paths := make([]string, len(typed))
		for i, entry := range typed {
			path, ok := entry.(string)
			Expect(ok).To(BeTrue(), "specPath entry %d is %T", i, entry)
			paths[i] = path
		}
		return paths
	default:
		ginkgo.Fail(fmt.Sprintf("invalid specPath type %T", value))
		return nil
	}
}

func runtimeArgumentNames(schema map[string]any, path string) []string {
	field := schema
	for _, name := range strings.Split(path, ".") {
		field = objectField(objectField(field, "properties", path), name, path)
	}
	raw, ok := field["x-clicky-arguments"].([]any)
	Expect(ok).To(BeTrue(), "runtime field %s has no native arguments", path)
	names := make([]string, 0, len(raw))
	for _, entry := range raw {
		argument, ok := entry.(map[string]any)
		Expect(ok).To(BeTrue(), "runtime field %s has malformed native argument", path)
		names = append(names, stringField(argument, "name"))
	}
	return names
}

func generatedField(root reflect.Type, name string) reflect.StructField {
	field, ok := root.FieldByName(name)
	Expect(ok).To(BeTrue(), "%s.%s", root.Name(), name)
	return field
}
