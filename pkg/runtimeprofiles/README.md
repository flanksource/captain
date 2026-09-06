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
resolved, err := api.ResolveSpecLayers(layers.Layers...)
if err != nil {
    return err
}
for _, warning := range resolved.Warnings {
    logger.Warnf("%s", warning)
}
```

`ResolveSpecLayers` composes in global → context → surface → user scope order, preserving order within a scope. It intersects restrictive catalogs, applies the strictest nonzero budget limits, and preserves every quota and raw layer in `Trace`. Compact model selectors retain their existing pin semantics: a prefix inside the effective model name wins over its sibling mode field. Model aliases and fallback names resolve only after composition. Final model, sandbox, and tool-policy refusals are errors; unsupported permission/resource capabilities produce separate `Warnings` for this compatibility release. No saved model defaults are loaded. Model-free compositions remain valid; execution requirements belong to `promptrun.Preflight`.

Use `api.ComposeSpecLayers` for forms and defaults before a complete request exists. Its distinct `ComposedSpec` exposes the structural `Spec`, `Constraints`, `Trace`, and `AllowsModel` query without claiming runtime validity. It shares the same fold as final resolution. Keep its raw `Trace` when adding a request, then call `ResolveSpecLayers` once for the final stack.

Layer resolution checks every declared primary and fallback runtime. Execution preflight separately checks the enabled candidates selected for the actual provider configuration. Disabling a candidate for one execution does not make an unsupported sandbox or tool policy valid in the declared profile.

Catalog failures preserve ownership: `*runtimeprofiles.OwnedLayersError` wraps invalid stored data and missing nested preset references. An absent or ambiguous top-level profile remains `ErrNotFound` or `ErrAmbiguous`. `Resolver` additionally wraps selection failures in `*SelectionError`, recording whether the request, prompt pin, or configured default selected the profile. Use `errors.As` and `errors.Is` instead of parsing error text.

Run the composition, ownership, and execution-admission examples without making AI calls:

```sh
go test ./pkg/api ./pkg/runtimeprofiles ./pkg/promptrun -ginkgo.no-color -ginkgo.succinct -count=1
```
