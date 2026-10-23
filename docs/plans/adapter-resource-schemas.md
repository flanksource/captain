# Adapter resource schema expansion

Status: implemented; final validation recorded in the execution report.

## Approved scope

Expand all registered API, Agent and CLI execution schemas and expose native provider configuration and reusable resources alongside portable Captain configuration. Include sandbox policies, named permission profiles, filesystem and network grants, hooks, MCP, agents, skills, plugins, model/environment settings and trusted Docker/Git Agent backend configuration. Keep provider execution wiring unchanged.

Expose a stable resource ID and kind on each catalog document, retain existing execution modes and URLs, and provide stateless schema validation. Compile local references without fetching external schemas. Preserve source versions and native constraints.

Use shared Clicky UI form annotations and controls for editing, enums, enum icons, documentation links, typed maps and unions. Offer Schema, Form, JSON and YAML views with independent local drafts per resource, recoverable malformed source and explicit schema validation. Selection, search and view belong in the URL. Drafts are local preview state and have no save/apply action.

## Implementation

`pkg/adapters/resources` contains pinned native source snapshots. The resource catalog projects reusable definitions with their transitive local references. Captain resources derive from the owning Go API models; trusted backend shapes follow their adapter option readers. `hack/adapterresources` imports pinned sources, completes Rust flatten-map declarations and annotates the schema graph without changing native enum values. Generated DTO union aliases preserve JSON marshaling methods.

The existing GET endpoint returns the expanded array. POST `/api/captain/ai/adapter-schemas/validate` takes `{id,value}` and returns `valid` and structured errors containing `instancePath`, `schemaPath` and `message`. Malformed requests return 400, unknown IDs return 404 and schema compilation failures return 500.

## Verification

Contract tests cover the complete registry matrix, source inventory, recursive annotations, self-contained schema compilation, invalid nested permissions and native DTO round trips. UI tests cover union selection, typed JSON, help links and independent recoverable resource drafts. Verify generation twice, focused Go and Vitest suites, Clicky UI check/build, Captain lint/build, and the exact development route in Chrome at desktop and compact widths.

See [the resource guide](../adapter-resource-schemas.md) for source capture, validation examples and operating limits.
