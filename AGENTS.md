# captain — agent notes

Shared ways of working, the gavel todo workflow, and global skills come from the root ~/.agents/AGENTS.md.

## Memory
- [Commons-db migrations (HCL + SQL)](.agents/memory/commons-db-migrations.md) — HCL for tables, post-HCL SQL for views/triggers/deferred constraints; verify with a seeded PostgreSQL smoke, not dry apply.
- [AI model fallback, recommendations & whoami catalogs](.agents/memory/ai-model-catalog-fallback.md) — IsFallbackEligible/ErrNoAPIKey classification in pkg/ai, per-family model retention in model_lists.go, keyless cmux backends in whoami.
- [AI schema shaping, generation config & runtime logging](.agents/memory/ai-schema-generation-logging.md) — SchemaJSONForBackend provider transforms vs local validation, EffortConfig in generation_config.go, agent:model[:effort] log identity.
- [Commit grouping for mixed Captain diffs](.agents/memory/commit-grouping.md) — group by semantic product slice not directory tree, tests travel with features, every file accounted for exactly once.
- [Session viewer, Codex/Claude parsing & model normalization](.agents/memory/sessions-viewer-metadata.md) — SessionBrowser/SessionInspector seams, shared Codex extraction, synthetic event tools, unified Title/InitialPrompt derivation.
- [History discovery, performance & bash categorization](.agents/memory/history-discovery-performance.md) — case-insensitive session-dir matching in both finders, prune Claude/Codex discovery early, pkg/bash priority-based classification.
- [Prompt CLI runs: JSON contract, context.dir, run tables & Codex app-server](.agents/memory/prompt-cli-runs.md) — AIPromptResult full-request JSON, context.dir normalization into cmd.Dir, -M comparison tables, app-server JSON-RPC seams.
- [Prompt workbench, serve UI, catalog & backend modes](.agents/memory/prompt-workbench-serve-ui.md) — captain serve hosts the workbench, catalog-backed family-first selectors, cmux-style backend modes land across four seams, rebuild linked clicky-ui dist.
- [Lint cleanup: gavel lint sweeps & betterleaks fixes](.agents/memory/lint-cleanup.md) — snapshot gavel lint baseline, split by ownership slices, betterleaks comment hits fixed by neutral wording.
