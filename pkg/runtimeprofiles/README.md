# Runtime profile composition

Use `Catalog.Layers` or `Resolver.Layers` to load reusable configuration before adding a prompt and request. They validate authored structures without choosing a model or checking a guessed runtime. A profile can contain only permissions, mode, effort, or budget; a later request can supply or replace its runtime.

Validate each owner's raw layers with `api.ValidateSpecLayers` before combining them. It returns `*api.LayerValidationError`, whose `Layer` and wrapped `Err` identify malformed metadata, model options, prompt attachments, permissions, workflow, or sandbox declarations. Invalid lower-priority values remain errors even when another layer would overwrite them. Missing model names and prompt bodies are valid in reusable fragments. Structural validation neither reads prompt/attachment files nor runs setup.

```go
layers, err := resolver.Layers(ctx, runtimeprofiles.ResolveOptions{
    RequestedProfile: "review",
    SurfaceLayers: []api.SpecLayer{
        api.PromptSpecLayer("review.prompt", document.Spec),
    },
    RequestLayers: []api.SpecLayer{
        api.RequestSpecLayer("request", request),
    },
})
if err != nil {
    return err
}
resolved, err := api.ResolveSpecLayers(api.ResolveSpecOptions{
    Layers: layers.Layers,
    Saved: &config.AI, // one snapshot loaded by the application boundary
    RequireModel: true,
})
if err != nil {
    return err
}
for _, warning := range resolved.Warnings {
    logger.Warnf("%s", warning)
}
```

`ResolveSpecLayers` composes in global → context → surface → user scope order, preserving order within a scope. It intersects restrictive catalogs, applies the strictest nonzero budget limits, and preserves every quota and raw layer in `Trace`. Compact model selectors retain their existing mode pin semantics: a prefix inside the effective model name wins over its sibling mode field. A higher layer's explicit effort overrides a lower compact effort and retains that higher layer's ownership. Model aliases and fallback names resolve only after composition. Final model, sandbox, and tool-policy refusals are errors; unsupported permission/resource capabilities produce separate `Warnings` for this compatibility release. The library never loads saved defaults from disk. Model-free compositions remain valid unless `RequireModel` is true; full execution admission still belongs to `promptrun.Preflight`.

Use `api.ComposeSpecLayers` with the same `ResolveSpecOptions` for forms and defaults before a complete request exists. Its distinct `ComposedSpec` exposes `Spec`, `Trace`, `Provenance`, and `Warnings` without claiming runtime validity. It shares the same fold and saved-default pass as final resolution. Unknown authored models remain visible in composition; provider-specific defaults wait until a provider is known. Keep the authored layers when adding a request, then call `ResolveSpecLayers` once for the final stack.

An optional `Saved *captainconfig.AIDefaults` snapshot fills gaps after every authored layer. Provider mode and effort defaults apply independently to the final primary and each fallback. The global compact selector contributes its mode, effort, and fallback chain only within its provider family. Authored primary temperature/cache settings, and same-family effort, retain the existing fallback inheritance rules. File-wide temperature, cache, budget, timeout, and ambient-memory toggles remain run-wide. With a saved snapshot, a missing token budget uses Captain's existing 4096 default and missing effort uses the selected model's catalog default when one is declared. A nil snapshot injects neither saved nor built-in defaults, supporting catalog and authoritative snapshot consumers.

JSON/YAML decoding retains explicitly authored `false`, zero, empty lists/maps, and null values. Go callers mark intentional zero values with `spec.WithExplicit("/noCache", "/budget/cost", "/fallbacks")`; ordinary nonzero fields and scalar pointers already count as supplied. Paths use the serialized JSON-pointer vocabulary, including `/permissions/mcp/disabled` for the native MCP toggle. Merging replaces explicit clears, preserves unrelated groups, and removes stale presence when a list is replaced. `WithoutSession` removes both conversation data and its presence metadata. `Spec.DecodeFields` exposes the complete native shape so enclosing decoders retain their unknown-field policy; Captain prompt documents reject unknown declarations, while hosts can report them as warnings.

`Provenance` maps effective field paths to a `FieldProvenance`. Its `Source` records the actual authored layer, saved config key, or catalog default that supplied the value; equal-valued request overrides still own their fields. `NormalizedBy` records later catalog normalization or a restrictive budget limit without relabeling the original source. CSV fallback entries point to the raw `/model` field that declared them, while explicit list entries retain their original `/fallbacks/N/...` source paths. The raw `Trace` contains no synthesized saved-default or normalized-request rows.

Applications that derive a working directory or sandbox mode can supply a pure `Normalize` callback. It receives an owned copy of the complete authored `Spec`, after compact grammar and any saved model/fallback selection, before saved mode, effort, and generation gaps are filled. It returns `SpecNormalization{Spec, Fields, Source}`. `Fields` explicitly identifies the derived paths; their existing source remains visible alongside `NormalizedBy`. Saved modes remain defaults for the primary and every fallback, so sandbox context can select their runtime; authored compact mode pins remain explicit constraints. Named sandbox references may remain partial during composition but must resolve to a concrete mode before final validation. This ordering lets provider defaults see the resulting runtime without a second layer fold. The callback must not construct providers, run setup, or persist state.

Malformed saved declarations fail before a request can hide them and return `*api.SavedDefaultsError` with a source and wrapped error. Missing required model selection remains an actionable configuration error. Missing modes on multi-mode providers warn separately and use the existing registry runtime default during the compatibility window; this applies to every fallback as well as the primary.

Layer resolution checks every declared primary and fallback runtime. Execution preflight separately checks the enabled candidates selected for the actual provider configuration. Disabling a candidate for one execution does not make an unsupported sandbox or tool policy valid in the declared profile.

Catalog failures preserve ownership: `*runtimeprofiles.OwnedLayersError` wraps invalid stored data and missing nested preset references. An absent or ambiguous top-level profile remains `ErrNotFound` or `ErrAmbiguous`. `Resolver` additionally wraps selection failures in `*SelectionError`, recording whether the request, prompt pin, or configured default selected the profile. Use `errors.As` and `errors.Is` instead of parsing error text.

## Built-in presets

Captain ships three runtime presets, embedded from `builtin/presets/*.yaml` and served by `NewBuiltinSource` (source kind `builtin`, id `builtin`, label `Built-in`, read-only, no profiles). `NewDefaultCatalog` registers the built-in source last, including file-only catalogs, so it is the lowest-precedence source. All three use `scope: context`.

| Name | Spec | Notes |
|---|---|---|
| `Edit` | `permissions: {mode: acceptEdits, presets: [edit]}` | The same posture as `--edit`: acceptEdits plus the `edit` bundle. |
| `Plan` | `permissions: {mode: plan, tools: {read: allow, shell: deny}}` | Edit and write stay available so the runtime's native plan mode can write its plan file. Sets no web policy. |
| `Read-only` | `permissions: {tools: {edit: deny, write: deny, shell: deny, web_search: deny, web_fetch: deny}}` | Sets no mode, so it combines with any posture; it blocks plan files too. Sets no read policy. |

None of them sets `mcp.disabled`, a sandbox, or a model. A built-in names only the tools it has an opinion about and never writes `auto`: `Spec.Merge` replaces `permissions.tools` key by key, so an `auto` entry would erase a same-key deny from a lower layer (for example Read-only's `web_search: deny` under `presets: [Read-only, Plan]`).

### Overriding a built-in

A database or file preset whose name matches a built-in case-insensitively overrides (shadows) it. File sources are `~/.config/captain/presets` (or `$XDG_CONFIG_HOME/captain/presets`), every `runtime.presetDirs` entry, and `<repo>/.captain/presets`. For example, `~/.config/captain/presets/plan.yaml`:

```yaml
name: Plan
description: Plan mode without the shell deny
scope: context
spec:
  permissions:
    mode: plan
```

The catalog applies the override everywhere a preset is read:

- `ListPresets` drops a shadowed built-in, so `findByName` and name uniqueness see the effective set. `GetPreset("plan")` returns the override.
- A stored built-in id (from a run, a profile, or a saved selection) resolves to the override, so `PresetLayers` and the resolved trace record the override's id and source.
- Creating or renaming a database or file preset to a built-in's name is allowed. Only built-ins can be shadowed: a database preset and a file preset with the same name are still `ErrAmbiguous` on lookup and `ErrNameTaken` on create.
- Deleting the override brings the built-in back. Profiles that name the override don't block that delete, since the built-in takes over the name; profiles that reference the override's id still do.
- Creating in, updating, or deleting from the built-in source returns `ErrReadOnly`, and `DeletePreset` checks that before counting references.
- Only reads follow a built-in id to its override. `UpdatePreset` and `DeletePreset` addressed by an encoded built-in id return `ErrReadOnly` whether or not an override exists, so a stale built-in row cannot rewrite or delete the override. Write the override through its own id or its name; a bare name targets the effective record.

## Portable tool names

A `permissions.tools` key is authored once and translated per runtime by `api.Tools.ForRuntime` (and `api.Permissions.ForRuntime`, which also returns the warnings); the resolved spec, trace, and preview keep the authored keys. The tool vocabulary of each agent is the `agentTools` table in `pkg/api/agent_resources.go`, which must declare every tool `pkg/ai/history` recognises for that agent.

A key is first checked for a `<provider>:<rule>` prefix such as `claude:Bash` or `gemini:read_file`, where the provider is any name `registry.ProviderByName` accepts. The key applies only on that provider and is dropped everywhere else. On that provider the rule after the prefix resolves exactly like an unprefixed key, so `openai:shell` is still the `shell` alias. Text before a colon that is not a provider (`Bash(npm run test:*)`) is part of the rule. Patterns are matched on the name before `(`.

On an agent runtime (cli, agent, cmux) the rule then resolves through the first step that matches:

1. An `mcp__…` name (any case) passes through as written, because MCP serves it.
2. The runtime's own tool name, matched case-sensitively, is kept as written, patterns included. When that name is also an alias (codex `shell` and `web_search`, gemini `web_fetch`), the other tools carrying the alias are added as bare names.
3. A shared alias, matched case-insensitively, expands to the bare name of every tool on the runtime that carries it.
4. Another agent's built-in, matched case-sensitively, expands through that tool's aliases.
5. The runtime's own tool name in another case is emitted in its canonical spelling (`bash(git log:*)` becomes `Bash(git log:*)` on claude).
6. Another agent's built-in in another case expands through its aliases.

A pattern has no meaning on a tool it did not name, so for steps 2 to 4 and 6 a patterned deny fails closed to the bare tools and a patterned allow adds nothing beyond the exact rule. Anything else is ignored, with a warning when the ignored policy is a deny. That includes names no agent declares: caller tools are governed by `toolPolicy` and tool preferences, not `permissions.tools`, on agent runtimes. `RequireToolPolicySupport` refuses `ask` on the authored keys everywhere, before translation.

On API mode, which ships no built-in tools, `mcp__…` names pass through, any agent's built-in or alias (any case) is ignored, and every other name is kept as a caller tool.

| Alias | claude | codex | gemini |
|---|---|---|---|
| `shell` | Bash, BashOutput, KillShell, Monitor | shell, exec, exec_command, write_stdin | run_shell_command |
| `read` | Read | (none; codex reads through shell) | read_file, read_many_files |
| `edit` | Edit, MultiEdit, NotebookEdit | apply_patch | replace |
| `write` | Write, NotebookEdit | apply_patch | write_file |
| `search` | Glob, Grep | | glob, search_file_content, list_directory |
| `web_fetch` | WebFetch | | web_fetch |
| `web_search` | WebSearch | web_search | google_web_search |
| `todo` | TodoWrite, TaskCreate, TaskGet, TaskList, TaskUpdate | update_plan | |

When several keys land on the same runtime tool, the strictest policy wins: `deny`, then `ask`, then `allow`, and `auto` has no opinion. Collisions can only tighten, because the layer fold forgets which layer wrote which key: a lower layer's `Bash: allow` never lifts a preset's `shell: deny`. To lift an inherited alias deny, write the same key (`shell: allow`) in a higher layer, or deselect the preset.

Portable allowlists: on runtimes without a per-tool filter (`registry.SupportsToolPolicy` is false, today codex and gemini), an allow is dropped when it reached a tool only through an alias or another agent's built-in, and also whenever the key, after any prefix and in any case, is spelled as an alias, even where the alias is also one of the runtime's tool names. So `shell: allow`, `Shell: allow` and `openai:shell: allow` are all inert on codex, and `web_fetch: allow` is inert on gemini. An allow of a runtime tool that is not an alias (`exec_command: allow`, `openai:apply_patch: allow`) is kept and refused there, because the runtime cannot enforce it. Denies are never dropped: `shell: deny` on codex becomes `shell`, `exec`, `exec_command` and `write_stdin` and is refused loudly.

Run the composition, ownership, and execution-admission examples without making AI calls:

```sh
go test ./pkg/api ./pkg/runtimeprofiles ./pkg/promptrun -ginkgo.no-color -ginkgo.succinct -count=1
```
