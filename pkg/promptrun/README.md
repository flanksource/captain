# Prompt run admission

Call `Preflight(Input) ([]string, error)` with the same complete input that will be passed to `Run`. It reads judge prompt files and validates declarations, attachments, deadlines, model selection, runtime policies, verifier registration, and commit ownership. It does not construct a provider, invoke a verifier factory or approval callback, run hooks or setup, emit events, or persist a run. `Run` uses the same admission check before dispatch.

```go
input := promptrun.Input{
    Resolved: resolved,
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

## Recording a run

Set `Input.Record` to have Captain file the run; a host never writes `captain_*` tables itself. Admission runs first, in one transaction: the `Placement` session (an existing `SessionID`, or a `Sessions` tree ensured through `hierarchy.EnsureTx`, whose last entry is the admission session), the prompt run with `rendered_spec` set to the plain JSON of `Resolved.Spec` and `metadata` holding `specTrace`, `specProvenance`, `specWarnings`, `runtimePresets` and `runtimeProfile` (empty entries omitted), then the host's `Link(ctx, tx, run)`. A `Link` error rolls everything back and nothing is dispatched. A recorder hook trailing setup marks the run running with the post-setup spec and the resolved runtime, the provider's session id is bound to the admission session and to its transcript session as soon as it is known, and verification progress, iterations, notices and the terminal state (`DefaultOutcome`, or `Recording.Outcome`) are written as the run goes. `Result.PromptRunID` names the row. A store error fails the run. `Admit(ctx, in)` is that admission on its own, dispatching nothing; `Run` admits through it. `Settle(ctx, db, runID, outcome)` files the outcome of a run that ended outside `Run` — a parked run whose session was continued from Captain's session page — as succeeded, waiting (parked again on a new envelope), failed or cancelled, replacing its result text, JSON and error; a finished run is `ErrRunFinished`, and a run settled into a terminal state has its pending tool approvals cancelled. `Fail` and `Cancel` are `Settle` with a reason. `RecordCompleted` files a run that executed outside `Run` through the same steps.

The host owns resolution and passes the final `ResolvedSpec.Spec` intact: a spec layer only ever supplies defaults, so the budget and posture the host resolved are the ones admission reads, and admission never clamps a temporary copy. A host that must not let a request widen a posture applies that posture as its last layer before resolving. A declared deadline outranks `Input.Timeout`. `Resolved.Spec.Budget.Cost` bounds the whole run; a provider's legacy `Config.Budget.Cost` fallback only bounds its individual calls and cannot replace that whole-run limit.

A supplied `Provider` owns its runtime and workspace; construction `Config` is ignored. Otherwise, `Config.Model` selects the constructed provider when present, with `Resolved.Spec.Model` used when absent. Request tuning is still validated because the runner dispatches it. Command/fixture-only verification needs no model; declared judge prompts need either the run provider or `Verify.Provider`. Fixture verification requires a registered fixture factory, which preflight checks without invoking. Providerless verification refuses a non-off sandbox declaration because no run provider would apply that isolation.

Invalid input, existing tool-policy refusals, unsupported sandbox isolation or native policy fields, missing fixture wiring, and broken judge declarations are errors. Newly diagnosed unsupported permission/resource settings and missing approval brokers produce warnings for this compatibility release. A disabled skill is omitted; a contradictory skill still explicitly loaded through `memory.skills` is diagnosed. These warnings are also logged by `Run`.

Preflight validates the runtime identity exposed by a supplied provider. Its private fallback chain, credentials, adapter-specific configuration, external service availability, and runtime launch failures remain the provider's responsibility. This API is execution admission; [runtime-profile composition](../runtimeprofiles/README.md) and saved-model default resolution remain separate contracts. The final layer resolver and Preflight share the same pure runtime capability checker; Preflight additionally checks the actual supplied or constructed execution runtime and its input-specific configuration.

Run the executable examples and focused admission regressions without making AI calls:

```sh
go test ./pkg/promptrun -run TestPromptRun -ginkgo.focus=promptrun.Preflight -ginkgo.succinct -ginkgo.no-color -count=1
```
