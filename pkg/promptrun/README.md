# Prompt run admission

Call `Preflight(Input) ([]string, error)` with the same complete input that will be passed to `Run`. It reads judge prompt files and validates declarations, attachments, deadlines, model selection, runtime policies, verifier registration, and commit ownership. It does not construct a provider, invoke a verifier factory or approval callback, run hooks or setup, emit events, or persist a run. `Run` uses the same admission check before dispatch.

```go
input := promptrun.Input{
    Request: resolved.Spec,
    Config: config,
    Timeout: 10 * time.Minute,
    Hooks: hostHooks,
    CallerOwnsCommits: true,
}
warnings, err := promptrun.Preflight(input)
if err != nil {
    return err
}
for _, warning := range warnings {
    logger.Warnf("%s", warning)
}
result, err := promptrun.Run(ctx, input)
```

The host owns resolution and passes the final `ResolvedSpec.Spec` intact: a spec layer only ever supplies defaults, so the budget and posture the host resolved are the ones admission reads, and admission never clamps a temporary copy. A host that must not let a request widen a posture applies that posture as its last layer before resolving. A declared deadline outranks `Input.Timeout`. `Request.Budget.Cost` bounds the whole run; a provider's legacy `Config.Budget.Cost` fallback only bounds its individual calls and cannot replace that whole-run limit.

A supplied `Provider` owns its runtime and workspace; construction `Config` is ignored. Otherwise, `Config.Model` selects the constructed provider when present, with `Request.Model` used when absent. Request tuning is still validated because the runner dispatches it. Command/fixture-only verification needs no model; declared judge prompts need either the run provider or `Verify.Provider`. Fixture verification requires a registered fixture factory, which preflight checks without invoking. Providerless verification refuses a non-off sandbox declaration because no run provider would apply that isolation.

Invalid input, existing tool-policy refusals, unsupported sandbox isolation or native policy fields, missing fixture wiring, and broken judge declarations are errors. Newly diagnosed unsupported permission/resource settings and missing approval brokers produce warnings for this compatibility release. A disabled skill is omitted; a contradictory skill still explicitly loaded through `memory.skills` is diagnosed. These warnings are also logged by `Run`.

Preflight validates the runtime identity exposed by a supplied provider. Its private fallback chain, credentials, adapter-specific configuration, external service availability, and runtime launch failures remain the provider's responsibility. This API is execution admission; [runtime-profile composition](../runtimeprofiles/README.md) and saved-model default resolution remain separate contracts. The final layer resolver and Preflight share the same pure runtime capability checker; Preflight additionally checks the actual supplied or constructed execution runtime and its input-specific configuration.

Run the executable examples and focused admission regressions without making AI calls:

```sh
go test ./pkg/promptrun -run TestPromptRun -ginkgo.focus=promptrun.Preflight -ginkgo.succinct -ginkgo.no-color -count=1
```
