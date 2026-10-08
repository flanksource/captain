---
name: agents-mapper
description: Create or refresh Captain's hand-authored native adapter JSON Schemas and generated Go DTOs under pkg/adapters. Use when mapping a provider's API, Agent protocol, or CLI execution options, auditing adapter coverage after an upstream change, or adding a registered runtime. Excludes cmux, runtime execution wiring, and catalog/API exposure unless the user explicitly adds them to scope.
---

# Agent Adapter Mapper

Build a source-backed inventory of the native options Captain can send through each registered API, Agent, and CLI execution surface. Keep the JSON Schemas authoritative and regenerate disposable Go DTOs from them.

Before authoring or refreshing a schema, read [references/schema-contract.md](references/schema-contract.md). It defines the file layout, provenance and support annotations, clicky-ui presentation vocabulary, generation command, and validation invariants.

## Workflow

1. Inspect `pkg/api/registry/providers.go` and derive the provider/mode matrix from the current registry. Include every registered `api`, `agent`, and `cli` mode; exclude `cmux`. A provider-specific request may limit edits, but the coverage test must still account for the complete matrix.
2. Add or update the focused Ginkgo contract test first so the intended schema addition or refresh fails before changing schema data.
3. Inventory the exact execution boundary separately for each mode:
   - CLI: capture the installed binary version and the help for the exact command Captain launches, then trace the argv builder in `pkg/ai/provider`.
   - Agent: inspect the pinned SDK or protocol request types and Captain's bridge/app-server request construction.
   - API: inspect the pinned provider SDK request/config types, Captain's request builder, and the official documentation for the exact generation endpoint.
4. Compare the upstream surface with Captain's implementation. Include every upstream option within that execution boundary and classify it as `mapped`, `managed`, or `unsupported`; never infer one mode's surface from another.
5. Hand-author or update `pkg/adapters/<provider>/<mode>.schema.json`. Use native wire names for API and Agent fields; use stable lower-camel semantic names for CLI properties and record the literal flag or positional argument in `x-captain-option.nativeName`.
6. Regenerate `pkg/adapters/zz_generated_types.go` from all adapter schemas with the repository-pinned `go-jsonschema` tool. Never edit generated code manually.
7. Run the schema contract, coverage, generation-drift, lint, and build gates. Re-read the final schemas and explicitly account for every registry entry and every upstream option before reporting completion.

## Source authority

Use current primary evidence. Local implementation proves what Captain does; pinned SDK/protocol types and versioned CLI help prove the native shape; official provider documentation resolves gaps. Record versions and references in each schema instead of relying on a prose summary in the skill.

The inventory covers the command, request, or protocol Captain uses to execute a model. Do not include unrelated login, account, administration, model-management, or server-lifecycle commands.

If sources disagree, expose the discrepancy and resolve it from the pinned runtime Captain actually builds or executes. Do not guess, copy a sibling mode, or silently omit the option. If correct generation is blocked by an unsupported JSON Schema construct, stop and follow the repository's workaround-approval rule rather than weakening the schema.

## Scope boundaries

- Generated structs are schema DTOs only. Do not replace provider request types or wire them into execution unless explicitly requested.
- Do not expose these schemas through `RuntimeCatalog`, `/api/captain/ai/prompt/schema`, or another endpoint unless explicitly requested.
- Do not move or rewrite `pkg/api/runtime_arguments.go` as part of schema inventory work.
- Keep automatic skill discovery enabled and preserve unrelated files under `.agents` and `pkg/adapters`.
