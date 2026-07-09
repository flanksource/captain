package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	clickyrpc "github.com/flanksource/clicky/rpc"
)

// buildPromptSchemaDocument is the pure assembler (no I/O), taking a probed
// adapter set so tests can drive it with a deterministic, network-free stub.
func buildPromptSchemaDocument(adapters []AdapterStatus) (map[string]any, error) {
	reflected, err := reflectedSchemas()
	if err != nil {
		return nil, err
	}

	specMap, err := unmarshalMap(reflected.spec)
	if err != nil {
		return nil, fmt.Errorf("decode spec schema: %w", err)
	}
	promptMap, err := unmarshalMap(reflected.prompt)
	if err != nil {
		return nil, fmt.Errorf("decode prompt schema: %w", err)
	}
	actionMap, err := unmarshalMap(reflected.promptAction)
	if err != nil {
		return nil, fmt.Errorf("decode promptAction schema: %w", err)
	}

	if err := injectSpecConditionals(specMap, adapters, reflected.args); err != nil {
		return nil, err
	}
	backends, err := buildBackendsCatalog(adapters, reflected.args)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"schemaVersion": 2,
		"source":        "captain prompt --schema",
		"spec":          specMap,
		"prompt":        promptMap,
		"promptAction":  actionMap,
		"backends":      backends,
		"models":        flatModels(adapters),
		"examples": map[string]any{
			"spec": promptSchemaExampleSpec(),
		},
	}, nil
}

// backendArgSchemasJSON reflects each backend's cmux "extra args" option struct
// into a JSON schema, keyed by backend. Backends without cmux options
// (CLIOptionsFor fails loud) are simply absent — this is the source of truth for
// "which backends have args".
func backendArgSchemasJSON() (map[api.Backend][]byte, error) {
	out := map[api.Backend][]byte{}
	for _, b := range api.AllBackends() {
		opts, err := api.CLIOptionsFor(b)
		if err != nil {
			continue // backend has no cmux args
		}
		raw, err := json.Marshal(clickyrpc.SchemaForStruct(opts))
		if err != nil {
			return nil, fmt.Errorf("reflect %s cli args: %w", b, err)
		}
		out[b] = raw
	}
	return out, nil
}

// buildBackendsCatalog renders every backend as a catalog entry: its kind, auth
// env vars, live auth/model status from the probe, and (where available) its
// args schema. This replaces the old duplicated claudeCLIArgs/codexCLIArgs keys.
func buildBackendsCatalog(adapters []AdapterStatus, argsByBackend map[api.Backend][]byte) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(adapters))
	for _, a := range adapters {
		b := api.Backend(a.Backend)
		entry := map[string]any{
			"backend":       a.Backend,
			"kind":          b.Kind(),
			"authenticated": a.Authenticated,
			"ready":         a.Ready(),
		}
		if env := api.AuthEnvVars(b); len(env) > 0 {
			entry["authEnvVars"] = env
		}
		if a.Binary != "" {
			entry["binary"] = a.Binary
		}
		if a.BinaryMissing != "" {
			entry["binaryMissing"] = a.BinaryMissing
		}
		if len(a.Models) > 0 {
			entry["models"] = a.Models
		}
		if a.ModelError != "" {
			entry["modelError"] = a.ModelError
		}
		if raw, ok := argsByBackend[b]; ok {
			argsMap, err := unmarshalMap(raw)
			if err != nil {
				return nil, fmt.Errorf("decode %s args schema: %w", b, err)
			}
			entry["args"] = argsMap
		}
		out = append(out, entry)
	}
	return out, nil
}

// injectSpecConditionals adds one JSON-Schema if/then per backend to the spec so
// that, once a backend is chosen, `cliArgs` is constrained to that backend's args
// ($ref into $defs, or forbidden for non-cmux backends) and `model` is restricted
// to that backend's available models. The allOf sits at the spec root beside the
// invopop $ref (both apply as in-place applicators under draft 2020-12); cmux arg
// schemas are added under the root $defs and referenced, so the same schema
// appears in backends[].args without source duplication.
func injectSpecConditionals(specMap map[string]any, adapters []AdapterStatus, argsByBackend map[api.Backend][]byte) error {
	defs, _ := specMap["$defs"].(map[string]any)
	if defs == nil {
		defs = map[string]any{}
		specMap["$defs"] = defs
	}

	allOf := make([]any, 0, len(adapters))
	for _, a := range adapters {
		b := api.Backend(a.Backend)
		thenProps := map[string]any{}

		if raw, ok := argsByBackend[b]; ok {
			name := argDefName(b)
			argsMap, err := unmarshalMap(raw)
			if err != nil {
				return fmt.Errorf("decode %s args schema: %w", b, err)
			}
			defs[name] = argsMap
			thenProps["cliArgs"] = map[string]any{"$ref": "#/$defs/" + name}
		} else {
			// Non-cmux providers ignore cliArgs; forbid it so a misplaced value
			// is a validation error rather than silently dropped.
			thenProps["cliArgs"] = false
		}

		if len(a.Models) > 0 {
			thenProps["model"] = map[string]any{"enum": toAnySlice(a.Models)}
		}

		allOf = append(allOf, map[string]any{
			"if": map[string]any{
				"required":   []any{"backend"},
				"properties": map[string]any{"backend": map[string]any{"const": a.Backend}},
			},
			"then": map[string]any{"properties": thenProps},
		})
	}
	specMap["allOf"] = allOf
	return nil
}

// flatModels is a convenience union of every available model across adapters,
// shaped like clicky-ui's ChatModel catalog while retaining the legacy backend
// and ready fields for older consumers.
func flatModels(adapters []AdapterStatus) []map[string]any {
	type entry struct {
		data     map[string]any
		backends []string
	}
	out := []entry{}
	positions := map[string]int{}
	for _, a := range adapters {
		provider := modelProviderForBackend(api.Backend(a.Backend))
		for _, model := range flatModelDetails(a) {
			id := strings.TrimSpace(model.id)
			if id == "" {
				continue
			}
			key := provider + "\x00" + id
			if idx, ok := positions[key]; ok {
				if !containsString(out[idx].backends, a.Backend) {
					out[idx].backends = append(out[idx].backends, a.Backend)
					out[idx].data["backends"] = out[idx].backends
				}
				if a.Ready() {
					out[idx].data["configured"] = true
					out[idx].data["ready"] = true
				}
				continue
			}
			label := strings.TrimSpace(model.label)
			if label == "" {
				label = id
			}
			backends := []string{a.Backend}
			positions[key] = len(out)
			out = append(out, entry{
				backends: backends,
				data: map[string]any{
					"id":         id,
					"label":      label,
					"provider":   provider,
					"reasoning":  modelSupportsReasoning(id),
					"configured": a.Ready(),
					"backends":   backends,
					"backend":    a.Backend,
					"ready":      a.Ready(),
				},
			})
		}
	}
	flat := make([]map[string]any, 0, len(out))
	for _, item := range out {
		flat = append(flat, item.data)
	}
	return flat
}

type flatModelDetail struct {
	id    string
	label string
}

func flatModelDetails(adapter AdapterStatus) []flatModelDetail {
	if len(adapter.ModelDetails) > 0 {
		out := make([]flatModelDetail, 0, len(adapter.ModelDetails))
		for _, model := range adapter.ModelDetails {
			out = append(out, flatModelDetail{id: model.ID, label: model.Name})
		}
		return out
	}
	out := make([]flatModelDetail, 0, len(adapter.Models))
	for _, id := range adapter.Models {
		out = append(out, flatModelDetail{id: id, label: id})
	}
	return out
}

func modelProviderForBackend(backend api.Backend) string {
	switch backend {
	case api.BackendClaudeAgent, api.BackendClaudeCLI, api.BackendClaudeCmux:
		return "claude-agent"
	case api.BackendCodexAgent, api.BackendCodexCLI, api.BackendCodexCmux:
		return "codex-cli"
	case api.BackendGemini, api.BackendGeminiCLI:
		return "googleai"
	default:
		return string(backend)
	}
}

func modelSupportsReasoning(id string) bool {
	token := strings.ToLower(strings.TrimSpace(id))
	if slash := strings.LastIndex(token, "/"); slash >= 0 {
		token = token[slash+1:]
	}
	token = strings.TrimPrefix(token, "models/")
	switch {
	case strings.HasPrefix(token, "claude-"),
		strings.HasPrefix(token, "opus"),
		strings.HasPrefix(token, "sonnet"),
		strings.HasPrefix(token, "haiku"),
		strings.HasPrefix(token, "gpt-"),
		strings.HasPrefix(token, "o1"),
		strings.HasPrefix(token, "o3"),
		strings.HasPrefix(token, "o4"),
		strings.HasPrefix(token, "gemini-"),
		strings.HasPrefix(token, "deepseek"):
		return true
	default:
		return false
	}
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

// argDefName is the $defs key for a backend's args schema, e.g.
// "claude-cmux" -> "claudeCmuxArgs".
func argDefName(b api.Backend) string {
	parts := strings.Split(string(b), "-")
	var sb strings.Builder
	for i, p := range parts {
		if i == 0 {
			sb.WriteString(p)
			continue
		}
		sb.WriteString(upperFirst(p))
	}
	sb.WriteString("Args")
	return sb.String()
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func unmarshalMap(raw []byte) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
