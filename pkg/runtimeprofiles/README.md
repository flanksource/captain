# Runtime profile composition

Use `Catalog.Layers` or `Resolver.Layers` to load reusable configuration before adding a prompt and request. They validate authored structures without choosing a model or checking a guessed runtime. A profile can contain only permissions, mode, effort, or budget; a later request can supply or replace its runtime.

Validate each owner's raw layers with `api.ValidateSpecLayers` before combining them. It returns `*api.LayerValidationError`, whose `Layer` and wrapped `Err` identify malformed metadata, constraints, model options, prompt attachments, permissions, workflow, or sandbox declarations. Invalid lower-priority values remain errors even when another layer would overwrite them. Missing model names and prompt bodies are valid in reusable fragments. Structural validation neither reads prompt/attachment files nor runs setup.

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

Use `api.ComposeSpecLayers` with the same `ResolveSpecOptions` for forms and defaults before a complete request exists. Its distinct `ComposedSpec` exposes `Spec`, `Constraints`, `Trace`, `Provenance`, `Warnings`, and the `AllowsModel` query without claiming runtime validity. It shares the same fold and saved-default pass as final resolution. Unknown authored models remain visible in composition; provider-specific defaults wait until a provider is known. Keep the authored layers when adding a request, then call `ResolveSpecLayers` once for the final stack.

An optional `Saved *captainconfig.AIDefaults` snapshot fills gaps after every authored layer. Provider mode and effort defaults apply independently to the final primary and each fallback. The global compact selector contributes its mode, effort, and fallback chain only within its provider family. Authored primary temperature/cache settings, and same-family effort, retain the existing fallback inheritance rules. File-wide temperature, cache, budget, timeout, and ambient-memory toggles remain run-wide. With a saved snapshot, a missing token budget uses Captain's existing 4096 default and missing effort uses the selected model's catalog default when one is declared. A nil snapshot injects neither saved nor built-in defaults, supporting catalog and authoritative snapshot consumers.

JSON/YAML decoding retains explicitly authored `false`, zero, empty lists/maps, and null values. Go callers mark intentional zero values with `spec.WithExplicit("/noCache", "/budget/cost", "/fallbacks")`; ordinary nonzero fields and scalar pointers already count as supplied. Paths use the serialized JSON-pointer vocabulary, including `/permissions/mcp/disabled` for the native MCP toggle. Merging replaces explicit clears, preserves unrelated groups, and removes stale presence when a list is replaced. `WithoutSession` removes both conversation data and its presence metadata. `Spec.DecodeFields` exposes the complete native shape so enclosing decoders retain their unknown-field policy; Captain prompt documents reject unknown declarations, while hosts can report them as warnings.

`Provenance` maps effective field paths to a `FieldProvenance`. Its `Source` records the actual authored layer, saved config key, or catalog default that supplied the value; equal-valued request overrides still own their fields. `NormalizedBy` records later catalog normalization or a restrictive budget limit without relabeling the original source. CSV fallback entries point to the raw `/model` field that declared them, while explicit list entries retain their original `/fallbacks/N/...` source paths. The raw `Trace` contains no synthesized saved-default or normalized-request rows.

Applications that derive a working directory or sandbox mode can supply a pure `Normalize` callback. It receives an owned copy of the complete authored `Spec`, after compact grammar and any saved model/fallback selection, before saved mode, effort, and generation gaps are filled. It returns `SpecNormalization{Spec, Fields, Source}`. `Fields` explicitly identifies the derived paths; their existing source remains visible alongside `NormalizedBy`. Saved modes remain defaults for the primary and every fallback, so sandbox context can select their runtime; authored compact mode pins remain explicit constraints. Named sandbox references may remain partial during composition but must resolve to a concrete mode before final validation. This ordering lets provider defaults see the resulting runtime without a second layer fold. The callback must not construct providers, run setup, or persist state.

Malformed saved declarations fail before a request can hide them and return `*api.SavedDefaultsError` with a source and wrapped error. Missing required model selection remains an actionable configuration error. Missing modes on multi-mode providers warn separately and use the existing registry runtime default during the compatibility window; this applies to every fallback as well as the primary.

Layer resolution checks every declared primary and fallback runtime. Execution preflight separately checks the enabled candidates selected for the actual provider configuration. Disabling a candidate for one execution does not make an unsupported sandbox or tool policy valid in the declared profile.

Catalog failures preserve ownership: `*runtimeprofiles.OwnedLayersError` wraps invalid stored data and missing nested preset references. An absent or ambiguous top-level profile remains `ErrNotFound` or `ErrAmbiguous`. `Resolver` additionally wraps selection failures in `*SelectionError`, recording whether the request, prompt pin, or configured default selected the profile. Use `errors.As` and `errors.Is` instead of parsing error text.

Run the composition, ownership, and execution-admission examples without making AI calls:

```sh
go test ./pkg/api ./pkg/runtimeprofiles ./pkg/promptrun -ginkgo.no-color -ginkgo.succinct -count=1
```
