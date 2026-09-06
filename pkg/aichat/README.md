# Chat runtime profiles

`RuntimeProfileProvider` supplies application-owned defaults and restrictions before the chat request selects its final runtime. Profiles may contain only permissions, budgets, or model options. Catalog endpoints can use this partial configuration without requiring an executable model.

## Migrating `RuntimeProfile.Resolved` to `RuntimeProfile.Composed`

This is a Go API change for applications implementing `RuntimeProfileProvider`. Replace the `Resolved: api.ResolvedSpec` field with `Composed: api.ComposedSpec`, and replace profile-time `api.ResolveSpecLayers` calls with `api.ComposeSpecLayers`. Keep the original layers: converting an already resolved `Spec` into a new layer loses authored provenance and may carry normalized runtime defaults into a later request.

For example, a provider can construct its result from application-owned layers:

```go
func applicationProfile(system string, layers []api.SpecLayer) (aichat.RuntimeProfile, error) {
    composed, err := api.ComposeSpecLayers(layers...)
    if err != nil {
        return aichat.RuntimeProfile{}, err
    }
    return aichat.RuntimeProfile{
        System: system,
        Composed: composed,
    }, nil
}
```

`ComposeSpecLayers` validates authored structures and merges their values and constraints. Its `Trace` retains the raw layers; its `Spec` is a partial projection with no promise of runtime capability validity. Supply the composition result intact, including `Trace`. A nonempty profile projection without its composition trace is rejected. `System` and `ProviderConfig` retain their existing meanings.

The chat service adds the explicit request to that raw trace and performs final resolution. Applications that own a different request pipeline use the same boundary:

```go
func resolveRequest(profile aichat.RuntimeProfile, request api.Spec) (api.ResolvedSpec, error) {
    layers := append([]api.SpecLayer(nil), profile.Composed.Trace...)
    layers = append(layers, api.RequestSpecLayer("request", request))
    return api.ResolveSpecLayers(layers...)
}
```

Call `ResolveSpecLayers` after the complete request is available. It resolves the effective model and fallbacks and applies runtime capability checks. Inspect its separate `Warnings` when implementing a custom pipeline; the chat service logs them before provider admission. Saved model defaults are not loaded by either composition API.

Malformed application-owned layers remain server errors even if a request would overwrite them. Invalid explicit request fields are client errors. A structurally valid partial profile can be completed or repaired by the final request. Missing nested preset references are owned configuration failures; only an absent or ambiguous requested top-level profile should be classified as an invalid caller selection.

See [runtime profile composition](../runtimeprofiles/README.md) for raw catalog loading, layer ordering, compact selector precedence, and typed ownership errors. Downstream providers using `Resolved`, including OIPA's settings-backed chat provider, must migrate before adopting the release containing this API change.

Run the chat composition and ownership coverage without making AI calls:

```sh
go test ./pkg/aichat -run 'TestAIChat|TestEnforceApprovalRuntimeProfile' -count=1 -ginkgo.no-color
```
