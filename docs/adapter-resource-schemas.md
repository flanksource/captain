# Adapter and resource schemas

Open `/adapter-schemas` in `captain serve --dev`. Select a provider and resource to inspect its schema or edit a local Form, JSON or YAML draft. Resource selection, view and search are shareable URLs; draft values remain in the mounted page and are discarded on reload. Switching resources retains each draft. Invalid JSON/YAML stays editable and disables form editing and validation until corrected.

“Validate schema” checks the document against its selected JSON Schema. It does not resolve profile inheritance, inspect credential files, check installed tools, enforce runtime capabilities, execute hooks or apply configuration. Execution-only managed/unsupported options and enrollment-managed agent records render read-only. Native resource schemas describe the provider's configuration independently of Captain's forwarding support.

## Catalog

The array returned by `GET /api/captain/ai/adapter-schemas` contains stable `id`, `kind`, `provider`, `title`, `description` and `schema` fields. Execution documents additionally carry `mode`. Existing `anthropic/api`, `openai/agent` and other execution IDs remain valid. Resource IDs include `openai/permission-profile`, `anthropic/sandbox`, `google/settings`, `captain/permissions`, `captain/preset`, `captain/docker-backend` and `captain/git-agent-backend`.

POST a value without saving or applying it:

```sh
curl -sS http://localhost:9020/api/captain/ai/adapter-schemas/validate \
  -H 'Content-Type: application/json' \
  -d '{"id":"openai/permission-profile","value":{"extends":"team-workspace","workspace_roots":{"/workspace":true}}}'
```

Errors include JSON-pointer instance and schema paths. A well-formed request with invalid values returns HTTP 200 and `valid:false`; malformed requests return 400, unknown IDs 404, and invalid catalog schemas 500. The endpoint has a 1 MiB request limit. No runtime or persistence call occurs.

## Sources and regeneration

Configuration snapshots record source versions, references, SHA-256 and top-level property counts in `x-captain-resource`. Execution schemas retain `x-captain-adapter` provenance, support annotations and source inventories. CLI inventories include every long option in the captured execution command help, including aliases. API inventories follow the module-pinned request types and nested definitions, preserving required fields, literal discriminators, native enums and typed maps.

Capture source files under `.tmp/adapter-schema-sources` from the repository root:

```sh
mkdir -p .tmp/adapter-schema-sources
curl -fL https://raw.githubusercontent.com/openai/codex/rust-v0.164.0/codex-rs/core/config.schema.json -o .tmp/adapter-schema-sources/codex-config.json
curl -fL https://raw.githubusercontent.com/google-gemini/gemini-cli/v0.64.0/schemas/settings.schema.json -o .tmp/adapter-schema-sources/gemini-settings.json
npm install --prefix .tmp/adapter-schema-sources/sdk --ignore-scripts @anthropic-ai/claude-agent-sdk@0.3.280 ts-json-schema-generator@2.9.0
.tmp/adapter-schema-sources/sdk/node_modules/.bin/ts-json-schema-generator --path hack/adapterresources/claude-types.ts --type CaptainClaudeSettings --out .tmp/adapter-schema-sources/claude-settings.json
.tmp/adapter-schema-sources/sdk/node_modules/.bin/ts-json-schema-generator --path hack/adapterresources/claude-types.ts --type CaptainClaudeAgent --out .tmp/adapter-schema-sources/claude-agent.json
.tmp/adapter-schema-sources/sdk/node_modules/.bin/ts-json-schema-generator --path hack/adapterresources/claude-types.ts --type CaptainClaudeMCP --out .tmp/adapter-schema-sources/claude-mcp.json
.tmp/adapter-schema-sources/sdk/node_modules/.bin/ts-json-schema-generator --path hack/adapterresources/claude-types.ts --type CaptainClaudePlugin --out .tmp/adapter-schema-sources/claude-plugin.json
codex app-server generate-json-schema --experimental --out .tmp/adapter-schema-sources/codex-protocol
claude --help > .tmp/adapter-schema-sources/claude-help.txt
codex exec --help > .tmp/adapter-schema-sources/codex-help.txt
gemini --help > .tmp/adapter-schema-sources/gemini-help.txt
go run ./hack/adapterresources
go generate ./pkg/adapters
go test ./pkg/adapters/...
```

Use Claude CLI 2.1.309, Codex CLI 0.164.0 and Gemini CLI 0.64.0 for these captures. Review source-version changes intentionally. The importer rejects missing native root shapes and newly uncovered CLI flags. Codex's generated schema omits `serde(flatten)` permission maps; their value schemas are supplied from `codex-rs/config/src/permissions_toml.rs` at the same release, with source references on each supplemented definition.

Captain backend JSON follows `sandbox.backends.<name>` in `~/.captain.yaml`: `kind` and backend option keys are siblings. Docker backend volume values are strings (`source:target[:ro]`), distinct from the structured volumes in `.container-sandbox.yaml`. Git Agent enrollment entries contain endpoint trust and credential **paths**, never credential contents. Its `waitTimeout` remains subject to positive-duration validation at runtime.

## Form annotations

Use `title`, `description`, `x-help.links`, `x-clicky-order`, `x-clicky-section`, `x-enum-labels`, `x-enum-display`, `x-enum-icons`, `x-array-display` and `readOnly` on the owning schema nodes. Documentation links are absolute HTTP(S) URLs. Icons resolve through Clicky UI's offline icon provider. Presentation annotations do not replace validation constraints or native wire names.

Clicky UI renders object/array/scalar unions with a branch selector and retains branch drafts. `format:json` explicitly requests a typed JSON editor for genuinely arbitrary JSON. Typed `additionalProperties` and `patternProperties` remain editable maps; recursive references are kept in the schema graph rather than expanded indefinitely. Native source schemas can evolve independently of Captain's executable option coverage.

## Live form smoke check

With the development server on port 9020, run `go run ./hack/adapterbrowser`. It uses `agent-browser --engine chrome --session captain-adapters-impl` to open every live catalog document in Form view, verifies the local-preview and validation controls, and fails on a render error. It does not change resource values or apply configuration. Close that browser session after inspection with `agent-browser --engine chrome --session captain-adapters-impl close`.

The expansion was checked against all 50 live form views, JSON/YAML draft retention and correction, server-side nested permission errors, documentation links and offline enum icons, plus desktop and 390px compact layouts. Focused Go contract/API tests, 485 shared form tests, Captain UI tests, scoped lint, Go and Clicky builds, and repeated-generation hashes passed. Repository-wide lint encounters unrelated duplicate `main` declarations under `hack/`; the production webapp build encounters the existing `RuntimePreset.order` mismatch with the pinned published Clicky package. The shared form improvements live in the sibling Clicky UI checkout and need its normal release/dependency update before shipping.
