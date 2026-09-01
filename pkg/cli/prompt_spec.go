package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/collections"
	clickyrpc "github.com/flanksource/clicky/rpc"
)

func applyPromptDefaults(req *ai.Request, cfg *ai.Config) error {
	savedCfg := loadSavedConfig()
	saved := savedCfg.AI
	promptModel := req.Model
	if promptModel.Name == "" {
		promptModel.Name = cfg.Model.Name
	}
	if promptModel.ID == "" {
		promptModel.ID = cfg.Model.ID
	}
	if promptModel.Mode == "" {
		promptModel.Mode = cfg.Model.Mode
	}
	if promptModel.Provider == nil {
		promptModel.Provider = cfg.Model.Provider
	}
	identity := selectModelIdentity(
		api.Model{Name: promptModel.Name, ID: promptModel.ID, Mode: promptModel.Mode, Provider: promptModel.Provider},
	)
	req.Name, req.ID, req.Mode, req.Provider = identity.Name, identity.ID, identity.Mode, identity.Provider
	if cfg.Model.Effort != api.EffortNone {
		// An effort-qualified model selector (for example agent:sol:high)
		// is model-local and intentionally overrides the request-wide flag/default.
		req.Effort = cfg.Model.Effort
	} else if req.Effort == "" {
		req.Effort = cfg.Model.Effort
	}
	resolved, err := applyProviderDefaults(req.Model, saved)
	if err != nil {
		return err
	}
	req.Model = resolved
	req.NoCache = req.NoCache || saved.NoCache
	if req.Budget.MaxTokens == 0 {
		req.Budget.MaxTokens = firstPositive(cfg.Budget.MaxTokens, saved.MaxTokens, 4096)
	}
	if req.Budget.Cost == 0 {
		req.Budget.Cost = firstPositiveFloat(cfg.Budget.Cost, saved.BudgetUSD)
	}

	cfg.Model = req.Model
	cfg.Budget = req.Budget
	cfg.NoCache = req.NoCache
	if isZeroSchemaRepair(cfg.SchemaRepair) {
		cfg.SchemaRepair = schemaRepairConfig(savedCfg.Prompts.SchemaRepair)
	}
	return nil
}

func mergeStringMaps(base, overlay map[string]string) map[string]string {
	if len(overlay) == 0 {
		return base
	}
	out := make(map[string]string, collections.SafeAdd(len(base), len(overlay)))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		out[k] = v
	}
	return out
}

func mergePresets(base, overlay []api.Preset) []api.Preset {
	if len(overlay) == 0 {
		return base
	}
	seen := make(map[api.Preset]bool, collections.SafeAdd(len(base), len(overlay)))
	out := make([]api.Preset, 0, collections.SafeAdd(len(base), len(overlay)))
	for _, preset := range base {
		if seen[preset] {
			continue
		}
		seen[preset] = true
		out = append(out, preset)
	}
	for _, preset := range overlay {
		if seen[preset] {
			continue
		}
		seen[preset] = true
		out = append(out, preset)
	}
	return out
}

func sortedStringKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		if key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func enabledResourcePolicies(in api.ResourcePolicies) api.ResourcePolicies {
	out := api.ResourcePolicies{}
	for _, key := range sortedStringKeys(in) {
		if in[key] == api.ResourceEnabled {
			out[key] = api.ResourceEnabled
		}
	}
	return out
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, item := range in {
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

func readRenderRequest(ctx context.Context, flags map[string]string) (PromptRenderRequest, error) {
	var req PromptRenderRequest
	if err := decodePromptBody(ctx, map[string]any{}, &req); err != nil {
		return PromptRenderRequest{}, err
	}
	if req.Variables == nil {
		req.Variables = map[string]any{}
	}
	if err := mergePromptActionFlags(&req, flags); err != nil {
		return PromptRenderRequest{}, err
	}
	return req, nil
}

func mergePromptActionFlags(req *PromptRenderRequest, flags map[string]string) error {
	if len(flags) == 0 {
		return nil
	}
	if raw := strings.TrimSpace(flags["vars"]); raw != "" {
		var vars map[string]any
		if err := json.Unmarshal([]byte(raw), &vars); err != nil {
			return fmt.Errorf("parse --vars JSON: %w", err)
		}
		req.Variables = vars
	}
	if v := strings.TrimSpace(flags["model"]); v != "" {
		ensureRenderSpec(req).Name = v
	}
	if v := strings.TrimSpace(flags["fallback"]); v != "" {
		ensureRenderSpec(req).Fallbacks = fallbackModelsFromFlags([]string{v})
	}
	if v := strings.TrimSpace(flags["mode"]); v != "" {
		ensureRenderSpec(req).Mode = api.RuntimeMode(v)
	}
	if v := strings.TrimSpace(flags["timeout"]); v != "" {
		ensureRenderSpec(req).Budget.Timeout = v
	}
	if v := strings.TrimSpace(flags["max-tokens"]); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid --max-tokens %q: %w", v, err)
		}
		ensureRenderSpec(req).Budget.MaxTokens = n
	}
	return nil
}

func ensureRenderSpec(req *PromptRenderRequest) *api.Spec {
	if req.Spec == nil {
		req.Spec = &api.Spec{}
	}
	return req.Spec
}

func decodePromptBody(ctx context.Context, flat map[string]any, dst any) error {
	if r, ok := clickyrpc.RequestFromContext(ctx); ok && r.Body != nil {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return fmt.Errorf("read request body: %w", err)
		}
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		if len(strings.TrimSpace(string(body))) > 0 {
			decoder := json.NewDecoder(strings.NewReader(string(body)))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(dst); err != nil {
				return fmt.Errorf("decode request body: %w", err)
			}
			return nil
		}
	}
	if len(flat) == 0 {
		return nil
	}
	data, err := json.Marshal(flat)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return fmt.Errorf("decode command body: %w", err)
	}
	return nil
}

func runtimeTimeout(raw string) time.Duration {
	timeout, _ := time.ParseDuration(raw)
	if timeout <= 0 {
		return 120 * time.Second
	}
	return timeout
}
