package aichat

import (
	"fmt"
	"sort"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	"github.com/flanksource/clicky/rpc"
	"github.com/spf13/cobra"
)

// ClickyToolset adapts a Cobra command tree into Genkit tools backed by clicky's
// in-process RPC executor. Operations are discovered once via the converter; each
// becomes a dynamic Genkit tool whose input schema is the operation's JSON schema.
type ClickyToolset struct {
	service  *rpc.RPCService
	executor *rpc.CommandExecutor
}

// NewClickyToolset converts rootCmd into an RPC service and wires an enabled
// in-process executor. SkipPreRun avoids re-running cobra root hooks per call.
func NewClickyToolset(rootCmd *cobra.Command) (*ClickyToolset, error) {
	service, err := rpc.NewConverter(rpc.DefaultConfig()).ConvertCommandTree(rootCmd)
	if err != nil {
		return nil, fmt.Errorf("convert command tree: %w", err)
	}
	executor := rpc.NewCommandExecutor(service, &rpc.ExecutorConfig{
		Enabled:    true,
		SkipPreRun: true,
		PathPrefix: rpc.DefaultConfig().PathPrefix,
	})
	return &ClickyToolset{service: service, executor: executor}, nil
}

// DefineTools registers every operation as a Genkit tool on g and returns them
// as ToolRefs ready for ai.WithTools.
func (t *ClickyToolset) DefineTools(g *genkit.Genkit) []ai.ToolRef {
	refs := make([]ai.ToolRef, 0, len(t.service.Operations))
	for i := range t.service.Operations {
		op := &t.service.Operations[i]
		schema := jsonSchema(op.Schema)
		tool := genkit.DefineTool[any, any](g, op.Name, op.Description,
			t.handlerFor(op),
			ai.WithInputSchema(schema),
		)
		refs = append(refs, tool)
	}
	return refs
}

// handlerFor returns the execute function for one operation. Model input arrives
// as map[string]any (per the schema); we split it into clicky positional args
// and string flags, then execute in-process.
func (t *ClickyToolset) handlerFor(op *rpc.RPCOperation) ai.ToolFunc[any, any] {
	positional := positionalParams(op)
	return func(_ *ai.ToolContext, input any) (any, error) {
		req := toExecutionRequest(input, positional)
		data, resp, err := t.executor.ExecuteCommand(op, req)
		if err != nil {
			return nil, fmt.Errorf("execute %s: %w", op.Name, err)
		}
		if resp != nil && !resp.Success {
			return nil, fmt.Errorf("operation %s failed (exit %d): %s", op.Name, resp.ExitCode, resp.Error)
		}
		return data, nil
	}
}

// positionalParams returns the parameter names declared "in: path" — these map
// to clicky positional Args rather than Flags.
func positionalParams(op *rpc.RPCOperation) []string {
	var names []string
	for _, p := range op.Parameters {
		if p.In == "path" {
			names = append(names, p.Name)
		}
	}
	return names
}

// toExecutionRequest splits a model-provided input map into positional Args
// (in declared order) and the remaining keys as string Flags.
func toExecutionRequest(input any, positional []string) *rpc.ExecutionRequest {
	m, _ := input.(map[string]any)
	req := &rpc.ExecutionRequest{Flags: map[string]string{}}
	if m == nil {
		return req
	}
	isPositional := make(map[string]bool, len(positional))
	for _, name := range positional {
		isPositional[name] = true
	}
	for _, name := range positional {
		if v, ok := m[name]; ok {
			req.Args = append(req.Args, stringify(v))
		}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if isPositional[k] {
			continue
		}
		req.Flags[k] = stringify(m[k])
	}
	return req
}

func stringify(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", x)
	}
}

// jsonSchema converts clicky's rpc.Schema into the generic JSON-Schema map that
// ai.WithInputSchema expects.
func jsonSchema(s rpc.Schema) map[string]any {
	props := map[string]any{}
	for name, p := range s.Properties {
		entry := map[string]any{"type": p.Type}
		if p.Description != "" {
			entry["description"] = p.Description
		}
		if len(p.Enum) > 0 {
			enum := make([]any, len(p.Enum))
			for i, e := range p.Enum {
				enum[i] = e
			}
			entry["enum"] = enum
		}
		if p.Default != nil {
			entry["default"] = p.Default
		}
		props[name] = entry
	}
	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(s.Required) > 0 {
		req := make([]any, len(s.Required))
		for i, r := range s.Required {
			req[i] = r
		}
		schema["required"] = req
	}
	return schema
}
