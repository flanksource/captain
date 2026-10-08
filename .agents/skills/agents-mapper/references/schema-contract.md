# Adapter Schema Contract

Use this contract whenever `$agents-mapper` creates, refreshes, or reviews a native adapter schema.

## Artifact layout

The hand-authored schemas are the source of truth:

```text
pkg/adapters/
├── anthropic/
│   ├── api.schema.json
│   ├── agent.schema.json
│   └── cli.schema.json
├── openai/
│   ├── api.schema.json
│   ├── agent.schema.json
│   └── cli.schema.json
├── google/
│   ├── api.schema.json
│   └── cli.schema.json
├── deepseek/
│   └── api.schema.json
├── generate.go
└── zz_generated_types.go
```

Derive the actual matrix from `registry.Providers()` rather than preserving this example blindly. Add newly registered API, Agent, or CLI surfaces and reject files for unregistered surfaces. Never add a cmux schema.

`generate.go` owns one `//go:generate` directive whose inputs list every schema in canonical provider/mode order. `zz_generated_types.go` is regenerated output and carries the generator's `DO NOT EDIT` header.

## Execution boundary and naming

A schema inventories every documented option accepted by the exact execution entrypoint Captain uses:

- API schemas mirror the request/config fields for the generation endpoint Captain calls.
- Agent schemas mirror the native SDK or protocol payload paths Captain sends during initialization, session/thread start, resume, and turn execution.
- CLI schemas cover the executable and subcommand Captain launches, including positional input and config overrides that affect that invocation. They exclude unrelated administrative subcommands.

API and Agent property names must match their native JSON wire names. CLI properties use stable lower-camel semantic names so generated Go identifiers remain useful; `x-captain-option.nativeName` carries the literal `--flag`, configuration key, environment variable, positional argument, or `stdin` name.

Prefix root titles and reusable `$defs` names with provider and mode, for example `AnthropicCLIOptions`, so all schemas can generate into one Go package without name collisions.

## Root schema

Every document is JSON Schema Draft 2020-12 and declares a closed object:

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "AnthropicCLIOptions",
  "description": "Native options accepted by the Claude CLI execution command used by Captain.",
  "type": "object",
  "additionalProperties": false,
  "x-captain-adapter": {
    "provider": "anthropic",
    "mode": "cli",
    "sources": [
      {
        "kind": "cli-help",
        "reference": "claude --help",
        "version": "<captured version>"
      },
      {
        "kind": "implementation",
        "reference": "pkg/ai/provider/claude_cli.go",
        "version": "<Captain commit>"
      }
    ]
  },
  "properties": {}
}
```

Replace every angle-bracket example value with captured evidence. Use source kinds `cli-help`, `sdk`, `protocol`, `official-docs`, or `implementation`. Record enough version detail to reproduce the inventory: binary version, module/package version, protocol version, documentation URL, or Captain commit as applicable.

Use `required` only when the native boundary requires a value on every request. Copy native defaults only when the source declares them. Preserve exact enum spellings, numeric bounds, array item types, and object openness. Use local top-level `#/$defs/...` references when reuse materially helps; ensure both clicky-ui and the pinned generator handle every construct used.

## Option support annotation

Every leaf option has one `x-captain-option` object:

```json
{
  "title": "Model",
  "description": "Model identifier supplied to the native runtime.",
  "type": "string",
  "x-clicky-section": "model",
  "x-clicky-order": 10,
  "x-icon": "sparkles",
  "x-captain-option": {
    "nativeName": "--model",
    "support": "mapped",
    "specPath": "model"
  }
}
```

The support values are:

- `mapped`: a caller-authored Captain value reaches this native option. `specPath` is required and names its canonical Captain Spec path.
- `managed`: Captain sets or derives the option. `note` is required and explains the owning behavior; omit `specPath` and set `readOnly: true`.
- `unsupported`: the native surface accepts the option but Captain neither maps nor manages it. `note` is required; omit `specPath` and set `readOnly: true` so a generic form does not imply the value can be applied.

For a native option supplied from more than one Spec path, make `specPath` an ordered string array. Do not classify an option from name similarity; cite the actual construction or translation path in the test failure or implementation notes.

Structural object nodes do not need `x-captain-option`, but every terminal property does. A schema must not contain unclassified native options.

## JsonSchemaForm presentation

Required inputs need persistent `title` and `description` text; placeholders are not labels. Prefer standard JSON Schema keywords before extensions.

Verify extensions against the current `JsonSchemaProperty` and runtime schema types in the installed or linked `@flanksource/clicky-ui` package. The commonly useful vocabulary is:

- `x-clicky-section`: one of `model`, `prompt`, `workspace`, `sandbox`, `permissions`, `environment`, or `cli`.
- `x-clicky-order` or object-level `x-order`: deterministic form order.
- `x-icon`: a runtime icon key already supported by clicky-ui's fallback icon registry.
- `x-enum-display`: `combobox`, `radio`, `grid`, or `segmented`; pair with `x-enum-labels`, `x-enum-icons`, `x-enum-descriptions`, or `x-enum-tones` only when the chosen display uses them.
- `x-array-display`: `filter-pills`, `accordion`, `cards`, `stacked`, or `list`; declare a complete `items` schema first.
- `x-help` and `x-help-display`: source-aware detail or dense-form help.
- `x-columns`, `x-col-span`, `x-layout`, and input prefix/suffix annotations only when they materially improve the rendered form.

Do not invent extension names under `x-clicky-*`, use an enum display value clicky-ui does not recognize, or reference an icon key without verifying it. Keep Captain mapping metadata under `x-captain-*`; clicky-ui ignores unknown extensions and preserves them for consumers.

## Go generation

Use the repository's pinned Go tool. If it is absent and schema generation is in scope, add the current vetted generator with:

```bash
go get -tool github.com/atombender/go-jsonschema@v0.24.1
```

The verified baseline is compatible with Captain's Go 1.26 module. Keep the version pinned; update it only as a deliberate dependency change.

Generate schema DTOs only:

```bash
go tool go-jsonschema --only-models --struct-name-from-title --package adapters --tags json --output pkg/adapters/zz_generated_types.go <all schema paths in canonical order>
```

Put the concrete, fully enumerated command in `pkg/adapters/generate.go`; `go generate` does not expand globs. Do not add validation methods or hand-written behavior to generated DTOs, and do not edit generated names or tags after generation. Change the schema or generator settings instead.

If `go-jsonschema` cannot faithfully represent a required native union, conditional, or reference, treat that as a blocked correctness issue. Do not flatten it into a weaker type, widen it to `any`, or maintain a hand-edited generated exception.

## Tests and gates

Add focused Ginkgo tests under `pkg/adapters` that:

1. derive every registered API, Agent, and CLI pair from the registry and compare it exactly with schema paths, excluding cmux;
2. compile every document as Draft 2020-12 and assert the root provider/mode metadata matches its path;
3. walk every leaf and enforce title, description, valid section, valid support status, and the `specPath`/`note`/`readOnly` invariants;
4. assert schema titles and `$defs` names are globally unique;
5. decode representative mapped, managed, and unsupported shapes through each generated root DTO without losing native JSON names.

After generation, run:

```bash
go generate ./pkg/adapters
git diff --exit-code -- pkg/adapters/zz_generated_types.go
go test ./pkg/adapters/...
make lint
make build
git diff --check
```

Use the generated-file diff command as the clean-tree or CI drift gate. While intentionally changing a schema, review the expected generated diff, regenerate a second time, and confirm the second run introduces no further changes before handoff. Do not add catalog, endpoint, provider execution, or browser tests while the task remains files-and-DTOs only.
