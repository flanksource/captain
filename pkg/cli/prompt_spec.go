package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api"
	clickyrpc "github.com/flanksource/clicky/rpc"
)

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
	if v := strings.TrimSpace(flags["runtime-profile"]); v != "" {
		req.RuntimeProfile = v
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
		*req.Spec = req.Spec.WithExplicit("/budget/maxTokens")
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

// defaultRunTimeout is the CLI's own deadline for a run whose spec declares no
// budget.timeout. It belongs at the call sites that need a bound — the value
// pkg/cli hands promptrun.Input.Timeout — not inside the parser, where it would
// stand in for a value the author got wrong.
const defaultRunTimeout = 120 * time.Second

// runtimeTimeout resolves a declared budget.timeout string through the same
// parser api.Spec validation uses. Zero means no bound was declared and the
// caller's own default applies; an unparseable or non-positive value is an
// error naming the field and the raw value. Substituting a default here made
// `budget.timeout: "2 minutes"` run to a deadline nobody asked for, reported as
// if the declared ceiling had been honoured.
func runtimeTimeout(raw string) (time.Duration, error) {
	return api.Budget{Timeout: raw}.ParseTimeout()
}
