# Unified approval callback

## Summary

Extend Captain's existing `CanUseTool` callback in place, so that one callback carries execution approvals, clarification questions, and MCP elicitation, and give it a name that says so.

- The types are renamed: `PermissionFunc` → `ApprovalFunc`, `PermissionRequest` → `ApprovalRequest`, `PermissionDecision` → `ApprovalDecision`, `Config.CanUseTool` → `Config.OnApproval`. The old names remain as `// Deprecated:` aliases (see "Deprecated aliases"), so no caller breaks.
- `ApprovalRequest` gains a `Kind` discriminator and one typed payload per kind.
- `ApprovalDecision` gains the few fields that `allow`/`deny`/`updatedInput` cannot express: interrupt, scope, and grants.
- Captain's broker still owns waiting, persistence, and resolution. Gavel opts in, shows pending requests, and submits decisions. Providers translate between their native protocols and the extended request.
- Every existing type, callback, row shape, HTTP body, and component prop keeps working unchanged. The only breaks are the behaviour changes that are the point of the work (BC-1 to BC-4). Code still using the deprecated names gets `staticcheck` SA1019 warnings until Phase 6 removes them.

## Approaches considered

| Approach | Pros | Cons | Decision |
| --- | --- | --- | --- |
| Keep `CanUseTool` and encode questions, command approvals, and grants as synthetic tool calls inside `Input` | No type changes at all | A question has no real tool call. Grants cannot be validated as untyped `Input`. Consumers must parse provider-specific synthetic names. | Rejected |
| Keep `CanUseTool` for tools, add a callback for each other approval family | Each callback has a focused input and result | Codex can ask for filesystem and network access in one request. Every new native family adds another callback and another broker/UI path. | Rejected |
| Expose each provider's native approval callbacks directly | Every native decision and payload is preserved | Consumers become provider-aware, and Gavel must implement separate persistence, UI, and lifecycle handling for Claude and Codex. | Rejected |
| Replace `CanUseTool` with a new, separately shaped `OnApproval` contract | Clean names and shapes | Breaks every Go caller, persisted row, HTTP body, event, and component prop for what is mostly a rename. Needs a translation layer for every boundary. | Rejected |
| Extend `PermissionRequest`/`PermissionDecision` in place and keep the old names | No source break at all | `CanUseTool` would also answer questions and elicitation, so the name misleads new callers | Rejected |
| **Extend in place, rename to `ApprovalFunc`/`ApprovalRequest`/`ApprovalDecision`/`OnApproval`, and keep the old names as deprecated aliases** | One consumer integration and one durable lifecycle. Typed answers, grants, and elicitation content. Names that match the behaviour. Type aliases make old and new names the same type, so nothing breaks and callers migrate at their own pace. | Two spellings coexist until Phase 6. `Config` and `callertools.Options` carry both fields for a while, and setting both is an error. | **Chosen** |
| Register one global callback and recover run identity through Gavel's `ExecutorContext` | Simple registration for a single host | Hides identity and cancellation outside the callback contract. Couples Captain to Gavel's execution model. | Rejected |

The callback is scoped to the admitted run. Its active turn context carries cancellation and deadlines; a service-lifetime `context.Background()` would let a pending decision outlive the turn. The broker stays in Captain, so other hosts can use the same API without Gavel-specific identity recovery.

## Decisions

- **Deny is a hard floor.** A tool or MCP tool that the resolved permission strategy denies is auto-rejected before `OnApproval` runs. That covers `api.PermissionPolicy.Resolve` returning `ToolPolicyDeny`, `Tools.DenyList()`, and tool mode `off`. No decision, scope, or grant can override a deny.
  - A denied request never creates a pending row, an event, or a UI prompt.
  - A `permissions` grant that overlaps a denied entry is rejected.
- **Plan mode is a hard floor.** Plan-only runs reach no side effect, whatever the callback returns.
- **Any callback may escalate.** Approving a non-denied request may exceed the run's initial sandbox posture. This needs no opt-in flag.
  - The `ApprovalFunc` doc comment states it.
  - Every escalating request carries `Escalates: true` and the requested boundary, so a host can see what it grants.
  - Native platform restrictions still apply.
- **No callback keeps native behavior**, with one exception. A native request whose current answer is malformed gets a valid fail-closed answer instead: Codex elicitation today receives `{"decision": ...}`, and it will receive `{action: "decline"}`.
- **Persistence goes through presets.** `turn` and `session` scopes are honoured only as ephemeral native scopes, where a provider supports them. Durable "always allow/deny" decisions do not live in approval rows or in provider-native caches, such as Codex `acceptWithExecpolicyAmendment` and `applyNetworkPolicyAmendment` or Claude `updatedPermissions` rules. They are written as updates to the permission preset layer. That is tracked as gavel TODO `592982ed`, and this plan never offers persistent amendments.
- **Plan exit stays separate.** `ExitPlanMode` inside a plan-mode run remains the terminal plan workflow (`ai.PlanTerminalPermission`, unchanged). Outside plan mode, `ExitPlanMode` (including cmux's plan dialog) is routed to the existing `plan_exit_approval` request kind, not treated as an ordinary tool.

## API: extend in place, rename, deprecate the old names

Existing fields and meanings are kept. `Broker.ClaimToolUseID`, `ai.PlanTerminalPermission`, `approval.ResolveInput`, and `promptrun.ApprovalOptions` keep their names; their signatures now say `ApprovalRequest`/`ApprovalDecision`, which are the same types as before.

### Deprecated aliases

| New (canonical) | Deprecated | Mechanism |
| --- | --- | --- |
| `api.ApprovalFunc` | `api.PermissionFunc` | `type PermissionFunc = ApprovalFunc`. A type alias makes it the identical type, so values and function literals pass either way. |
| `api.ApprovalRequest` | `api.PermissionRequest` | Type alias |
| `api.ApprovalDecision` | `api.PermissionDecision` | Type alias |
| `ai.ApprovalFunc`, `ai.ApprovalRequest`, `ai.ApprovalDecision` | `ai.PermissionFunc`, `ai.PermissionRequest`, `ai.PermissionDecision` | Type aliases of the `api` types, as today |
| `Config.OnApproval` | `Config.CanUseTool` | A struct field cannot be aliased, so both fields exist for now. Providers never read either directly. They call `Config.Approvals() (ApprovalBinding, error)`, which fails when both are set, as a loud conflict rather than a silent precedence. A `CanUseTool` callback comes back wrapped in the legacy shim below. |
| `callertools.Options.OnApproval` | `callertools.Options.CanUseTool` | The same two-field rule and legacy shim, checked in `callertools.New` |
| `approval.Broker.OnApproval` | `approval.Broker.CanUseTool` | Delegates to `OnApproval`. The broker is the callee, not a callback registration, so there is no legacy skip: old callers build requests without `Kind`, and an empty `Kind` is treated as `tool`. |
| `observation.Recorder.ApprovalBroker` | `observation.Recorder.PermissionBroker` | Delegates to `ApprovalBroker`. The function it returns is wrapped in the legacy shim. |

Rules:

- Every deprecated identifier carries `// Deprecated: use <New>. Removed in the unified-approval Phase 6.` so `staticcheck` SA1019 flags each remaining use.
- Captain's own module moves to the new names in Phase 1. After Phase 1, `rg -w "CanUseTool|PermissionFunc|PermissionRequest|PermissionDecision|PermissionBroker" --type go` matches only the alias declarations and their tests.
- Persisted JSON, HTTP bodies, and event fields contain no Go type names, so the rename does not touch them.
- The Claude bridge's `can_use_tool` method and the SDK's `canUseTool` option are native protocol names, and they stay.
- The type aliases cannot log; only SA1019 reports them. The four deprecated entry points above (the two fields and the two methods) go through the compatibility shim, which does log.

### Compatibility shim for the deprecated entry points

A callback registered through a deprecated entry point was written against the old contract, which only ever delivered tool-shaped requests. Letting it suddenly receive Codex command, file, and permission approvals, or elicitations, would silently turn an auto-approving legacy callback into a sandbox escalator (BC-1). The shim runs such callbacks in **legacy mode**:

- **Routing.**
  - `ApprovalRequest` gains `LegacyContract bool` (`json:"-"`). Providers set it on requests the old contract already delivered: every Claude `canUseTool` request (including `AskUserQuestion`), caller tools, genkit/openai, cmux, and Codex `requestUserInput`.
  - `api.LegacyApprovalFunc(fn, entryPoint)` forwards those requests unchanged. The old callback sees the same `Tool`/`Input`/`ToolUseID`/`SessionID` as today, and its `{Allow, Message, UpdatedInput}` answer is valid.
  - For every other request it returns `api.ErrLegacyApprovalSkipped` (wrapped with the kind, tool, and `ToolUseID`) without calling `fn`. The providers treat that error, and only that error, by taking their existing no-callback path: the Codex posture answer for commands and file changes, no extra grants for permissions, and `decline` for elicitation. That is exactly what the run got before this change.
- **Codex policy.** `ApprovalBinding.Legacy` tells the Codex provider to keep sending the string `approvalPolicy` instead of the `granular` form (G19). A legacy run then does not start raising request kinds that would only be answered natively anyway.
- **Logged warnings.** Both are emitted through `commons/logger`.
  - *At setup:* one `WARN` per process per entry point, guarded by a `sync.Once` keyed on the entry point:
    `captain: Config.CanUseTool is deprecated; use Config.OnApproval (removed in unified-approval Phase 6). Legacy mode: Codex command, file-change and permission approvals, and elicitations, are answered natively and are not sent to this callback.`
    The two methods add the caller's `file:line` from `runtime.Caller(1)`. The fields cannot, so they name the field.
  - *Per skipped request:* one `WARN` per run per kind (for example `captain: legacy CanUseTool callback skipped a command request (exec_command, appr_…); answered natively with decline. Switch to Config.OnApproval to handle it.`), then `DEBUG` for further skips of that kind in the same run.
  - No approval row or `EventPermission` is created for a skipped request, as before.
- **Marking.** The shim, the sentinel error, `LegacyContract`, `ApprovalBinding.Legacy`, and both warnings carry `// COMPAT(unified-approval): remove in Phase 6`, so `rg "COMPAT\(unified-approval\)"` lists everything Phase 6 deletes.
- **Who is affected.** Captain's in-module callers move to `OnApproval` in Phase 1, so aichat and `promptrun`'s broker binding (which Gavel uses through `promptrun.ApprovalOptions`) get the full new kinds. Only out-of-module code still setting `CanUseTool`, or calling the deprecated methods, runs in legacy mode.

### `api.ApprovalRequest`

| Field | Status | Meaning |
| --- | --- | --- |
| `Tool` | kept | Always set. It is the native tool name (`Bash`, `Edit`, `WebFetch`, `AskUserQuestion`, `exec_command`, `apply_patch`, `write_stdin`, `request_permissions`, `mcp__<server>__<tool>`), or `Elicitation` for elicitation. Codex questions keep `AskUserQuestion`, as today. Callbacks that switch on `Tool` keep working. |
| `Input` | kept | The raw native input, as today |
| `ToolUseID`, `ToolUseIDGenerated` | kept | `ToolUseID` is the native request id; see the idempotency table. It is unchanged for every source that uses it today. |
| `SessionID` | kept | |
| `Kind ApprovalKind` | new | `command`, `tool`, `filesystem`, `network`, `permissions`, `question`, `elicitation`, or `plan`. Providers must set it, and `Validate` rejects an empty `Kind`. |
| `TurnID`, `Reason`, `Escalates`, `Interruptible`, `SupportedScopes []ApprovalScope` | new | Computed by the provider for each request, not for each kind |
| `Info *api.ToolInfo` | new, `json:"-"` | The input for the deny pre-check, including MCP hints. It reuses `ToolInfo` and is not persisted. |
| `Command *CommandApproval` | new | `{Command, Cwd, Unsandboxed, Stdin *{Terminal, Chars}, ProposedPolicy []string}` |
| `Filesystem *FilesystemApproval` | new | `{Operation, Paths, Changes}` |
| `Network *NetworkApproval` | new | `{Host, Protocol, URL, Command}` |
| `Permissions *api.NativeSandboxPolicy` | new | Reuses the spec sandbox vocabulary: `Filesystem.WritableRoots`/`ReadableRoots` and `Network.Access`/`AllowedDomains`. Codex glob and special paths (`tmpdir`, `project_roots`) resolve to concrete paths against `cwd`; a glob that cannot resolve is rejected. |
| `Questions []api.TerminalQuestion` | new | Reuses `TerminalQuestion`, which gains an additive `Secret bool` field |
| `Elicitation *ElicitationApproval` | new | `{Server, Mode (form or url), Message, Schema, URL, ElicitationID}` |

Exactly one payload is set, and it matches `Kind`; `Validate` enforces this. `tool` kind carries no extra payload: `Tool`/`Input`/`Info` already describe it.

### `api.ApprovalDecision`

| Field | Status | Meaning |
| --- | --- | --- |
| `Allow`, `Message` | kept | |
| `UpdatedInput` | kept | Also carries answers and elicitation content, using the conventions that already exist: question answers are `UpdatedInput["answers"]`, as parsed today by `api.AnswersForQuestions` for both Claude and Codex, and elicitation content is the content object itself. |
| `Interrupt bool` | new | Deny and interrupt the turn |
| `Scope ApprovalScope` | new | Empty means `request`, otherwise `turn` or `session` |
| `Grants *api.NativeSandboxPolicy` | new | The granted subset for `permissions`. Nil together with `Allow` grants everything requested. |

The conceptual actions map onto these fields:

| Action | Fields |
| --- | --- |
| approve | `Allow: true` |
| respond | `Allow: true` plus `UpdatedInput` (answers or form content). A url-mode elicitation uses no content. |
| deny | `Allow: false` |
| cancel | `Allow: false, Interrupt: true` |

`ApprovalDecision.Validate(ApprovalRequest)` in `pkg/api` is the single validator. It rejects:

- `Interrupt` on a request that is not `Interruptible`
- a `Scope` that is not in `SupportedScopes`
- `Grants` on anything but `permissions`, or grants outside the requested set or overlapping a deny rule
- answers to unknown questions
- form content that does not match `Schema`, or content on a url-mode elicitation

It runs at resolve time, so a bad decision is a 4xx to the host and never becomes a terminal row. The provider runs it again before translating the decision.

`api.Event` keeps `Tool`, `Input`, `ToolCallID`, and `ApprovalID` for `EventPermission` and gains `Request *ApprovalRequest`. `approval.ResolveInput` and `database.ResolveToolApprovalRequestInput` keep `Approved`, `UpdatedInput`, and `Reason`, and gain `Interrupt`, `Scope`, and `Grants`.

### Action and scope matrix

| Kind | Claude Agent | Codex app-server | cmux | genkit / openai / caller tools |
| --- | --- | --- | --- | --- |
| `command` | approve (updated input), deny, cancel. Scope: request only. | approve, deny (`decline`), cancel. Scope `session` → `acceptForSession`. No `turn` scope and no updated input. | approve (Enter), deny/cancel (Escape) | n/a |
| `tool` | approve (updated input), deny, cancel | Connector tool approvals: approve, deny, cancel | approve, deny/cancel | approve (updated input), deny |
| `filesystem` | Claude file tools: approve (updated input), deny, cancel | `fileChange`: approve, deny, cancel. `session` → `acceptForSession`. | n/a | n/a |
| `network` | Claude web tools: approve (updated input), deny, cancel | A command with `networkApprovalContext`: approve, deny, cancel, keeping the command context | n/a | n/a |
| `permissions` | n/a | A subset of the requested grants. Scope `turn` or `session`. | n/a | n/a |
| `question` | respond or deny, via `AskUserQuestion` | respond only. Deny returns an RPC error (current behaviour). | n/a | n/a |
| `elicitation` | SDK `onElicitation` bridged to the host: respond (`accept` + content), deny (`decline`), cancel | respond (`accept` + content), deny (`decline`), cancel | n/a | n/a |
| `plan` | `ExitPlanMode` outside a plan-only run: approve (as written), deny (keep planning, `Message` as feedback), cancel | n/a | the plan dialog: approve (Enter), deny (Escape) | n/a |

The Codex proposals `proposedExecpolicyAmendment` and `proposedNetworkPolicyAmendments` are kept on the request for display, but they are never offered as actions (see Decisions).

### Native ID and idempotency

The key format stays `provider:<run>:<ToolUseID>` (and `mcp:<credential>:<tool call>` for caller tools). Only the value placed in `ToolUseID` is chosen per source, so rows pending across an upgrade still match:

| Source | `ToolUseID` |
| --- | --- |
| Claude canUseTool | `tool_use_id` (unchanged) |
| Codex command / network | `approvalId` when present, otherwise `itemId`. Codex leaves `approvalId` null for ordinary shell approvals and sets it exactly when one item raises several approvals, so G8 still holds. |
| Codex writeStdin | `approvalId`, which is required: a missing one is an RPC error |
| Codex fileChange / permissions / requestUserInput | `itemId` (unchanged for questions) |
| Codex elicitation | `elicit:<JSON-RPC request id>` (there is no item) |
| Claude elicitation | `elicit:<bridge instance>:<request counter>`. agent.ts generates both, because JSON-RPC ids restart when the bridge process restarts. |
| Caller tools (Claude and Codex) | The provider tool-call id, claimed when generated (unchanged) |
| cmux | `<surface ref>:<sequence>:<digest of the dialog>`; the per-session sequence keeps two identical prompts from replaying one answer |
| genkit / openai | The tool call id (unchanged) |

### Examples

[unified-approval-callback-examples.md](unified-approval-callback-examples.md) shows every kind as it reaches `OnApproval`: the native request, the extended `ApprovalRequest`, a `ApprovalDecision`, and the native answer. The examples are mined from Captain's `captain_turn_requests` rows, Codex rollout approval requests, and Claude transcripts. Network permissions and elicitation never occur in the mined history, so those examples are derived from the Codex schema and labelled that way.

## Gaps

| # | Gap | Evidence | Resolution |
| --- | --- | --- | --- |
| G1 | Codex answers command/file approvals from posture alone, so the callback is never consulted | `codex_appserver_approval.go:62-78` | Route them through `OnApproval` when a callback is attached. Keep the posture answer when none is. |
| G2 | Nothing prevents a callback from approving something a deny rule covers | No pre-callback policy check on the Codex, cmux, or Claude canUseTool paths | A shared deny pre-check on `Info` before the broker, plus a validator check on grants |
| G3 | Claude caller tools never reach Claude's canUseTool: agent.ts auto-allows `mcp__<server>__` tools, and approval happens in `callertools.Runtime` | `agent.ts:298-305`, `callertools/runtime.go:246-265` | `callertools.Runtime` sets `Kind: tool` itself, for both providers |
| G4 | Claude's caller-tool runtime uses `context.Background()` with no `ContextForCall` | `claudeagent/caller_tools.go:35-36` vs `codex_appserver.go:505-510` | Mirror Codex's `ContextForCall` |
| G5 | Caller-tool approval is cut short by a 5-minute `context.WithTimeout`, and Claude sets no `ApprovalTimeout` | `callertools/runtime.go:250`, `claudeagent/caller_tools.go:35-39` | Parse `Permissions.ApprovalTimeout` for Claude, and let the broker's expiry be the one bound |
| G6 | With a callback attached and no active turn, Claude allows everything | `claudeagent/turn.go:289-291` | Fail closed |
| G7 | The secret-question rejection is in the Codex provider, so no callback can see a secret question | `codex_appserver_approval.go:124-126` | Pass `TerminalQuestion.Secret` through, and reject secrets in the durable broker only |
| G8 | A second approval on one Codex item would reuse its `itemId` and conflict | `tool_approval_store.go:136,163` | `approvalId` for command approvals (idempotency table) |
| G9 | cmux has no tool call id, so the durable broker rejects it. A callback error leaves the dialog up. | `cmux/stall.go:306-315,278-281`, `tool_approval_store.go:118-121` | Surface+hash `ToolUseID`. Escape on error or expiry. |
| G10 | Codex elicitation falls into the default branch and returns an invalid `{"decision": ...}` | `codex_appserver_approval.go:76-77` | A typed handler, with `decline` when no callback is attached |
| G11 | Codex `writeStdin` command approvals and the legacy `execCommandApproval`/`applyPatchApproval` are unmapped | Codex app-server schema (codex-cli 0.157.1) | `writeStdin` → `command` with `Stdin`. The legacy methods get an explicit RPC error. |
| G12 | Claude's bridge forwards only `{tool, input, tool_use_id}` and returns no `interrupt` | `agent.ts:308-312`, `turn.go` `canUseToolParams`/`canUseToolResult` | Carry `interrupt`. Claude supports only request scope, because persistence goes through presets. |
| G13 | Storage handles only `tool`/`input` requests | `tool_approval_store.go` | Keep `kind = 'tool_approval'` for every approval kind and persist the full extended request in `request` JSONB (additive keys). `response` gains `interrupt`, `scope`, `grants`. No enum, constraint, or view migration. |
| G14 | Validation happens after the row is terminal | `tool_approval_store.go:180-247` | Call `ApprovalDecision.Validate` from `approval.Resolve` |
| G15 | Duplicate `EventPermission`: Claude and cmux emit one, then the broker emits another | `turn.go:309`, `cmux/stall.go:275`, `broker.go:179` | Emit once, from the seam that binds `OnApproval` (see BC-3) |
| G16 | Callers that need to set `Kind` | genkit `tools.go:165`, openai `tools.go:60`, `callertools/runtime.go:251`, `aichat/execution_database.go:151` | Set `Kind: tool`. Preflight and capability text are unchanged. |
| G17 | Consumers render only tool-shaped requests | Gavel `pr/ui/todo_approvals.go:53-91`, captain webapp `sessionApprovals.ts`, clicky-ui `SessionViewer`/`SessionInspector`/chat | Additive fields and props (Phase 4). Old consumers keep rendering the `Tool`/`Input` view. |
| G18 | Claude never wires the SDK's `onElicitation` | The option exists in `adapters/zz_generated_anthropic_agent.go:123`; `agent.ts` never sets it | An `elicit` bridge request answered through `OnApproval` as `elicitation`. `decline` when no broker is attached. |
| G19 | The Codex permissions and elicitation kinds never fire. Captain sends a string `approvalPolicy`, and the `granular` form defaults `request_permissions` to false. None of the 16,865 mined requests was either kind. | `codex_appserver_protocol.go:416,508`; `AskForApproval.granular` | With a callback attached, send `granular` with `sandbox_approval`, `rules`, `request_permissions`, and `mcp_elicitations` all set to true |
| G20 | `item/fileChange/requestApproval` carries no files or patch | `FileChangeRequestApprovalParams` | Join with the `item/started` fileChange item by `itemId`. Set `Escalates` when a changed path is outside the writable roots. |

## Breaking changes

Reusing the existing types, and keeping deprecated aliases for the renamed ones, leaves no schema, wire, or props break, and no source break before Phase 6. What remains is behaviour (BC-1 to BC-4) plus the deprecation (BC-5):

| # | Change | Who notices | Handling |
| --- | --- | --- | --- |
| BC-1 | The callback receives kinds it never saw before: Codex command/file/permissions/elicitation, and Claude elicitation (G1, G18, G19). An existing callback that approves everything now also grants escalations and all requested permissions. That follows from the "any callback may escalate" decision. | Callbacks registered through `OnApproval`: `aichat`'s broker, Gavel runs with approvals on (through `promptrun`), and any new host | Release notes. Callbacks that need to discriminate switch on `Kind`. Callbacks still registered through the deprecated `CanUseTool` run in legacy mode and never see the new kinds, with a logged warning (see "Compatibility shim"). |
| BC-2 | Runtime answers change: Codex runs with a callback wait for a human instead of the sandbox posture answer. Denied tools never appear as requests. Claude with no active turn fails closed. Claude caller-tool approvals end with the turn and follow `approvalTimeout` rather than 5 minutes. cmux sends Escape on error or expiry. Codex elicitation without a callback answers `decline`. | Hosts relying on the old answers | Release notes. These are the point of the change. |
| BC-3 | One `EventPermission` per request, emitted by the seam that binds `OnApproval` (the broker, or `promptrun`'s wrapper around a direct callback). Claude and cmux stop emitting their own event without an `ApprovalID`. | Hosts that react to the first, id-less event | The event keeps every existing field. Hosts using a direct callback still get one event. |
| BC-4 | The Claude bridge protocol gains `interrupt` and `elicit` | A stale agent.ts process after upgrade | A `protocolVersion` handshake refuses an old bridge, with an error saying to restart |
| BC-5 | Renamed Go identifiers (`CanUseTool` → `OnApproval`, `Permission*` → `Approval*`) | Downstream Go code: it gets SA1019 lint warnings and runtime deprecation `WARN`s now, and a compile break only in Phase 6 | Deprecated aliases and the legacy-mode shim until the Phase 6 gate passes |

## Phases

### Phase 1: Extended types and deny floor (captain)

- [x] Write failing Ginkgo tests first:
  - `Validate` rejects each case listed under API
  - a denied tool never reaches the callback
  - `Allow` cannot un-deny
  - a grant that overlaps a deny is rejected
  - plan-mode denial
  - an old-style `{Allow, Message, UpdatedInput}` decision is still valid for `tool` kind
  - `Config.Approvals()` returns `OnApproval` unwrapped, or the deprecated `CanUseTool` wrapped with `Legacy: true`, and errors when both are set. `callertools.New` applies the same rule.
  - Legacy shim: a `LegacyContract` request reaches the old callback unchanged. A request without it returns `ErrLegacyApprovalSkipped` without calling the callback. Each provider answers a skipped request exactly as with no callback: Codex posture decline, no extra grants, elicitation `decline`.
  - Legacy Codex runs keep the string `approvalPolicy`.
  - Warnings (captured with a test logger):
    - one setup `WARN` per entry point per process, including `file:line` for the methods
    - one skip `WARN` per run per kind, then `DEBUG`
    - no warning at all for `OnApproval`
  - a function literal typed as the deprecated `PermissionFunc` is assignable to `OnApproval` (the alias is the identical type)
  - `Broker.CanUseTool` and `Recorder.PermissionBroker` delegate to their new counterparts
- [x] Rename to `ApprovalFunc`/`ApprovalRequest`/`ApprovalDecision` in `pkg/api`, with type aliases for the old names. Mirror the renames in the `ai.*` aliases. Add `Config.OnApproval` and `Config.Approvals()`, `callertools.Options.OnApproval`, `Broker.OnApproval`, and `Recorder.ApprovalBroker`. Keep the old identifiers with `// Deprecated:` comments (see "Deprecated aliases"). Add `api.LegacyApprovalFunc`, `api.ErrLegacyApprovalSkipped`, `ApprovalRequest.LegacyContract`, `ApprovalBinding`, and the warnings, all marked `COMPAT(unified-approval)`. Use `gopatch` to move every in-module call site to the new names.
- [x] Add `Kind`, the common fields, and the typed payloads to `api.ApprovalRequest`. Add `Interrupt`, `Scope`, and `Grants` to `api.ApprovalDecision`. Add `ApprovalDecision.Validate`. Add `TerminalQuestion.Secret` and `Event.Request`. Document the broadened meaning and escalation on `ApprovalFunc`.
- [x] Shared deny pre-check on `ApprovalRequest.Info`, called from every provider seam before the broker.
- [x] Set `Kind: tool` in genkit, openai, `callertools`, and aichat (G16).
- Implementation notes (as built):
  - **One normalization point.** `api.NewProvider` calls `Config.resolveApprovals()`, which folds a deprecated `CanUseTool` into a legacy-wrapped `OnApproval` and records `Config.LegacyApprovals()`. Providers read `cfg.OnApproval` only. `callertools.New` applies the same rule to its own options.
  - **Two separate grant checks.** `ApprovalDecision.Validate(req)` checks a grant against what was requested. `NativeSandboxPolicy.GrantConflict(grant)` checks it against the run's denied roots and domains. The Codex permissions handler calls both (Phase 2), because only the provider holds the run's sandbox policy.
  - **Deny pre-check location.** It is `aitools.ApprovalDenial(ResolveOptions, req)`, reusing the existing tool resolver, and it is wired into Claude `canUseTool` and cmux. Caller tools, genkit, and openai already drop denied tools through `ResolveDefinitions`. Codex is wired in Phase 2 with its new handlers.
  - **Broker checks.** The broker validates every request (`req.Validate()`), so an empty `Kind` is refused. It also sets `Event.Request` on the permission event.
  - **Delegation shape.** `Broker.CanUseTool` treats an empty `Kind` as `tool` rather than applying the legacy skip; see the aliases table. `Recorder.PermissionBroker` returns a legacy-wrapped callback.
  - **Lint.** The compat aliases point straight at the new types (`= api.ApprovalFunc`), so captain's own code has no SA1019 findings and needs no `nolint`.
  - **Verified.** `make build` and `make lint` are clean. The `pkg/api`, `pkg/ai`, `pkg/ai/approval`, `pkg/ai/callertools`, `pkg/ai/observation`, `pkg/ai/tools`, claudeagent, genkit, openai, codex provider, `pkg/promptrun`, `pkg/aichat` and `pkg/database` suites pass. Gavel compiles unchanged against this checkout, and its stop specs that call `broker.CanUseTool` pass.
  - **Failures that predate this work.** cmux and cli model tests expect `claude-opus-5` where the catalog now resolves `claude-opus-5-5`. Session-liveness tests list OS processes, which the sandbox blocks. Gavel's `host_defaults` spec fails on the openai tool-policy refusal.
- Backwards compatibility: additive only. Every existing caller compiles and behaves the same through the deprecated aliases. `make lint` stays green in Captain, because its own module no longer uses the deprecated names.

### Phase 2: Provider translation (captain)

- [x] Claude:
  - Set `Kind` by operation: Bash → `command` (`Escalates`/`Unsandboxed` for `dangerouslyDisableSandbox`), file tools → `filesystem`, web tools → `network`, `AskUserQuestion` → `question`.
  - Carry `interrupt` through agent.ts (G12).
  - Fail closed when no turn is active (G6).
- [x] Caller tools: add `ContextForCall` and `ApprovalTimeout` for Claude (G3–G5).
- [x] Codex:
  - Route command, fileChange, permissions, requestUserInput, and elicitation through `OnApproval` (G1, G10, G11).
  - Send the `granular` approval policy (G19).
  - Join fileChange approvals to their item (G20).
  - Take `Interruptible`/`SupportedScopes` from `availableDecisions` when it is present.
  - Translate decisions to the native vocabulary, and never offer policy amendments.
- [x] Claude elicitation (G18):
  - [x] Check the request and result shapes against the pinned SDK typings (`@anthropic-ai/claude-agent-sdk` 0.3.280).
  - [x] Add an `elicit` method to `protocol.ts` and `agent.ts`, with `options.onElicitation` forwarding over `callHost`. Wire it whenever a broker is attached.
  - [x] Handle `elicit` in `turn.go` `onRequest` on the active turn's context. With no active turn, or on a bridge error, answer `cancel`.
  - [x] Add `protocolVersion` to the bridge `initialize` reply, and refuse an older bridge (BC-4).
- [x] The deny pre-check does not apply to elicitation, because it asks for input rather than running a tool. `Elicitation.Server` is recorded for the preset TODO.
- [x] Elicitation tests for both providers (`claudeagent/elicitation_ginkgo_test.go`, `codex_appserver_elicitation_ginkgo_test.go`):
  - form content that validates against `Schema`, and content that fails
  - url mode with no content
  - `decline` vs `cancel` translation
  - no callback attached → `decline`
  - turn cancelled while pending → `cancel`
  - two elicitations at once on one run
- [x] cmux: `ToolUseID` is `<surface>:<seq>:<digest>` (the sequence keeps two identical prompts from replaying one answer), Escape on callback error or expiry, deny pre-check before the callback, no provider-side event (G9).
- [x] `ExitPlanMode` outside a plan-only run is a `plan` approval (decided 2026-09-29). `ApprovalKindPlan` carries `Plan *TerminalPlan`, parsed by the shared `api.TerminalPlanFromInput`. Approve implements the plan as written, and the decision takes no `updatedInput`. Deny keeps planning, with `Message` as the feedback. Cancel interrupts where the request is interruptible (Claude). A plan-only run still ends on `ExitPlanMode` through `ai.PlanTerminalPermission`, and nobody is asked.
  - Claude: `describeToolInput` maps `ExitPlanMode` to `plan`. Missing plan text is refused before the callback runs.
  - cmux: the dialog shows only its options, so `SessionAccumulator` records the last `ExitPlanMode` call from the session log. The approval carries that plan and is keyed by its `tool_use` id. Each logged call answers one dialog (`takePlan`), so a second plan dialog waits for its own call instead of replaying the first answer. A plan-only run dismisses the dialog without needing the log. A deny presses Escape; the feedback message is not typed into the terminal.
  - Storage stays `kind = 'tool_approval'` with `"kind":"plan"` in the request document. The unused `plan_exit_approval` enum value is left alone.
  - Consumers: clicky-ui renders the plan as markdown, with Approve plan, Keep planning, Send feedback and Cancel. Gavel and the webapp need no change, since approve and deny with a message already map. The Gavel `v0.0.63` replay covers approve and feedback.
- Claude slice notes (as built): the can_use_tool handling moved to `approval_request.go`, and the elicitation handler to `elicitation.go` / `elicitation.ts`. `agent.ts` was split (`messages.ts`) to stay under 500 lines. The bridge protocol version is 2. Missing command, path or host inputs deny loudly rather than fall back to `tool`. An invalid elicitation decision answers `cancel` with a WARN. Shapes were checked against SDK 0.3.280 `sdk.d.ts` (`ElicitationRequest`, `OnElicitation`, `PermissionResult`).
- [ ] Follow-up: `elicitHost` ignores the SDK abort signal, so an aborted elicitation stays pending until the turn ends. A `cancel_elicit` notification from the bridge would release it sooner.
- Codex slice notes (as built):
  - Handlers are split across `codex_appserver_{approval,command_approval,permissions_approval,elicitation,question}.go`, with one shared `resolveApproval` flow: plan floor, then deny floor, then no-callback answer, then turn check, then validate, callback and translate.
  - The deny floor also applies with no callback attached, so a denied tool is declined even under a bypass posture. A denied question is an RPC error, since Codex has no native decline for questions.
  - `availableDecisions` and `additionalPermissions` exist only in the experimental schema, which Captain requests. `availableDecisions` drives `Interruptible` and `SupportedScopes`, and an unoffered decision is refused.
  - `writeStdin` params carry no characters, so `Stdin.Terminal` is the parent `itemId` and the raw params stay in `Input`.
  - Unknown server-request methods get `-32601` instead of an invalid `decision` answer. `openai/userVerification` elicitation is an RPC error when a callback is attached.
  - Permission paths: `root` → `/`, `slash_tmp` → `/tmp`, `tmpdir` → `$TMPDIR` (refused if unset), `project_roots` → the workspace roots. A real glob, `minimal`, `unknown`, or `access: deny` is refused loudly. Grants go back as concrete path entries, and `request` scope becomes the native `turn`.
  - fileChange items are cached from `item/started` and `item/fileChange/patchUpdated`.
  - `SetPermissionMode` now keeps the run's tool policy, workspace and sandbox.
  - A recorded `approvalPolicy` can now be the granular object; `history.CodexPayload.PermissionMode` already reads it as `granular` → default.
- [ ] Follow-up: `additionalPermissions` on a Codex command approval is granted by accepting the command but is not shown as a grant. It should become an `Escalates` detail or a permissions payload.
- [x] Emit `EventPermission` once, from the binding seam (G15, BC-3): the broker sets `Event.Request`, and `promptrun.announceApprovals` (through `Config.WrapApprovals`) announces direct host callbacks. cmux no longer emits its own event; the Claude and Codex slices remove theirs.
- [x] Tests: native response translation for every matrix row, active-turn context and cancellation, mixed permission grants, the escalation flag.
- Backwards compatibility: with no callback, providers keep their current native answers, apart from the fail-closed fixes in G6 and G10.

### Phase 3: Broker and storage (captain)

- [x] Persist the full extended request in `request` JSONB. Keep `tool`/`input` as they are, and add `kind` plus the payload key. `ResolveToolApprovalRequest` stores `interrupt`, `scope`, and `grants` in `response`, and `decide()` reads them back. No migration.
- [x] `approval.Resolve` calls `ApprovalDecision.Validate` against the stored request (G14).
- [x] Broker: reject secret questions (G7, done early in Phase 2 because the Codex provider stops rejecting them), and keep concurrent waits, cancellation, expiry, and idempotent resolution.
- [x] Tests: typed request persisted beside tool/input; session scope, interrupt and grants round-trip to the provider; invalid decision refused with the row left pending; pre-kind row resolves as a tool; secret question refused.
- [x] Tests: concurrent requests of mixed kinds on one run (a command and a question pending together, each answered on its own, the run held in `waiting` until the last), and a second approval on the same Codex item (distinct `approvalId`s become distinct rows). Both are in `pkg/ai/approval/broker_kinds_ginkgo_test.go`.
- Implementation notes (as built): `CreateToolApprovalRequestInput.Approval` is optional (Gavel's tests still create rows the old way), and its Tool/Input/ToolUseID must match the row's fields. Documents are JSON-normalized, so a repeated identical answer compares equal to the stored one. `approval.Resolve` now always reads the row first, to validate against it. `aichat`'s resolve path (`execution_database_authority.go`) now goes through `approval.Resolve` too, which gained `ExpectedTurnID` for it, so it validates decisions the same way (Phase 5).
- Backwards compatibility: existing rows are valid extended requests with no `kind`. Reads treat a row without `kind` as `tool`, which is the only kind that was ever stored. Row readers that ignore the new keys are unaffected.

### Phase 4: Backwards compatibility check (captain, clicky-ui, Gavel)

There are no shims to write or remove. This phase proves that the old consumers keep working unchanged against the new release.

- [x] Checked-in fixtures of Gavel `v0.0.63`'s approval query, its `POST /api/todos/session/approve` bodies (`approve`, `deny`, `respond` with `input.answers`), and its `EventPermission` reads. The captain integration tests replay them against a `command`, a `question`, and an `elicitation` request and assert the same results as today:
  - `respond` + `input` is approve-with-input
  - question answers pass through `UpdatedInput["answers"]`
  - form content passes through `UpdatedInput`
- [x] clicky-ui (done in Phase 5): render tests showing that an old-shape `SessionPendingTool` and the old `{allow, message, answers}` decision still work after the additive props land.
- [x] Old-bridge refusal test (BC-4): `claudeagent/bridge_protocol_ginkgo_test.go`, from the Claude slice.
- [x] A compile-only fixture that uses every deprecated identifier against the new release (the old-name call sites Gavel `v0.0.63` has). It must build, and it is excluded from SA1019 lint through its build tag.
- [x] A behaviour fixture for a legacy `CanUseTool` callback that approves everything, run against a Claude run and a Codex run. The Claude run gets the same requests and answers as today. The Codex run's command, file-change, and permission requests get the pre-change posture answers, and the callback is never called for them. The setup and skip warnings are logged.
- [x] Release notes for BC-1 to BC-4, plus the rename table and the Phase 6 removal plan.
- Implementation notes (as built):
  - Gavel replay: `pkg/ai/approval/testdata/gavel-v0.0.63.json` holds seven dashboard interactions. `gavel_compat_ginkgo_test.go` reads each row and event through only `tool`/`input`/`approvalId` (Gavel's `todoApprovalOf`), then resolves the way `resolveApproval` does. A deny with no message still reaches the provider as `"tool call denied"`, as today.
  - Compile fixture: `pkg/api/compattest/deprecated.go` sits behind the `unified_approval_compat` build tag. `compattest_ginkgo_test.go` compiles it with `go vet -tags unified_approval_compat`, and default-tag lint never sees it.
  - Legacy behaviour: `provider/codex_appserver_legacy_ginkgo_test.go` covers Codex. It runs one approve-all legacy callback against a workspace run (decline, decline, no grant) and a full-access run (accept, accept, no grant), and shows the callback still answers a question. Claude's legacy path is covered by `claudeagent/approval_deny_ginkgo_test.go` (tool requests carry `LegacyContract`) and `elicitation_ginkgo_test.go` (a legacy callback declines an elicitation). The two warnings are asserted in `pkg/api/approval_compat_ginkgo_test.go`, where the log hook is reachable.
  - Release notes: `docs/src/pages/agents/approvals.mdx` gained "Request kinds" and "Migrating from `CanUseTool`" sections. The docs pages now use only the new names. The page's `Broker` field listing still shows `OnWaiting`/`OnRunning`, which predate this work (the struct has `OnRunState` and `Deadline`). That is left for a docs pass.
  - The clicky-ui old-props render test moves to Phase 5: there are no additive props to test against until that phase adds them.
- Backwards compatibility: this phase is the check. Any fixture failure is a break, and the fix goes in the new code, never in a shim.

### Phase 5: Consumers adopt the new kinds

The captain, clicky-ui, and Gavel releases are now independent. Each can ship in any order, because every change is additive.

- [x] clicky-ui:
  - `SessionPendingTool` gains optional `kind` and `request`, and `SessionToolDecision` gains optional `interrupt`, `scope`, `grants`, and `content`. This is a minor version.
  - `SessionViewer`/`SessionInspector` render each kind: answer controls for questions, the exact grants with subset selection for permissions, `JsonSchemaForm` for form-mode elicitation, and a link with Done / Decline / Cancel for url mode.
  - Cancel is shown only when the request is `Interruptible`.
- [x] Captain webapp `sessionApprovals.ts`.
- [x] Gavel (all but the `go.mod` bump, which waits for the captain tag):
  - `GET /api/todos/session/approvals` adds `kind` and `request`.
  - `POST …/approve` adds optional `scope` and `grants`, and a `cancel` action (→ `Interrupt`).
  - `respond` keeps its meaning, and form content travels as `input`.
  - Bump `captain` in `go.mod`.
  - Move to the new names (`OnApproval`, `ApprovalRequest`, `ApprovalDecision`), so Gavel's lint has no SA1019 findings.
- Implementation notes (as built):
  - **clicky-ui** (`packages/ui/src/data/ai/`):
    - `approval-request.ts` mirrors `ApprovalRequest`, `NativeSandboxPolicy` and the decision fields.
    - Each kind renders in `SessionViewer.approval-body.tsx` and `SessionViewer.approval-forms.tsx`, with `JsonSchemaForm` for form elicitation.
    - `SessionInspector.approvals` passes an optional fourth `decision` argument.
    - Vitest covers the old untyped shape and the old decisions, which was the Phase 4 clicky-ui item. There are 13 stories built from the mined examples.
    - The package version is not bumped, and `dist` is rebuilt.
  - **Captain webapp**:
    - `sessionApprovals.ts` `approvalDecisionBody()` sends `interrupt`, `scope` and `grants`. Answers and form content go in `updatedInput`.
    - It keeps a local mirror of the decision type until clicky-ui is released.
  - **Captain Go**:
    - `pkg/aichat/approval_http.go` accepts `interrupt`, `scope` and `grants`, and maps `ErrInvalidResolution` to 400.
    - `session.Request` gains `request`, the stored document, so the Inspector's Approvals tab gets typed requests.
    - Rows the aichat caller-tool path writes stay kindless and read as `tool`.
  - **Gavel Go**:
    - GET adds `kind` (`tool` when the row has none) and `request`.
    - POST adds `scope`, `grants` and `cancel`. `ErrInvalidResolution` maps to 400, so a deny that also sends `input` now returns 400 instead of 409.
    - The question guard keys on `kind`.
    - The permission notification carries `kind`.
    - No deprecated names remain.
    - `todo_session_approve_ginkgo_test.go` now uses `dbtest.ForGinkgo`.
  - **Gavel UI**: `TodoSession.tsx` maps the new decision fields to the approve body. Deny plus interrupt maps to `cancel`, and content maps to `respond` with `input`.
  - **Release steps left**:
    - Tag captain, then bump Gavel's `go.mod`. Gavel's `go mod tidy` also needs that bump.
    - Publish clicky-ui (minor version), then bump Gavel's catalog in `pr/ui/pnpm-workspace.yaml` and the webapp dependency. Then switch the webapp's local decision mirror to the clicky-ui import.
    - Gavel's `tsc` stays red until the clicky-ui release. It was already red on other unreleased clicky-ui imports.
  - **Not verified**: pixel and layout checks. Chrome could not launch in the sandbox. The DOM of the permissions-subset and elicitation-form stories was checked with lightpanda.
  - **Gaps left**:
    - No masked input for `secret` questions; the broker rejects them anyway.
    - Codex questions still show Reject.
- Backwards compatibility: additive fields only. Old clients keep working.

### Phase 6: Remove the deprecated aliases (captain)

- [ ] Entry gate:
  - Gavel's `go.mod` pins a captain release from Phase 5 or later.
  - Across the Flanksource sibling repos (captain, Gavel, clicky, anything else that imports `captain/pkg/api` or `pkg/ai`), `rg -w "CanUseTool|PermissionFunc|PermissionRequest|PermissionDecision|PermissionBroker" --type go` finds nothing outside the alias declarations.
  - No `is deprecated; use` or `legacy CanUseTool callback skipped` warning appears in the Gavel server or `captain serve` logs for one release.
- [ ] Delete the type aliases, `Config.CanUseTool`, `callertools.Options.CanUseTool`, `Broker.CanUseTool`, and `Recorder.PermissionBroker`. `Config.Approvals()` reduces to returning `OnApproval`; inline it if nothing else needs it.
- [ ] Delete the compatibility shim: `LegacyApprovalFunc`, `ErrLegacyApprovalSkipped` and each provider's handling of it, `ApprovalRequest.LegacyContract`, `ApprovalBinding.Legacy` and the Codex string-policy branch, and both warnings. `rg "COMPAT\(unified-approval\)"` must return nothing.
- [ ] Delete the Phase 4 compile-only and legacy-behaviour fixtures.
- Backwards compatibility: this is the break, a source-only one. Rows, HTTP bodies, events, and props never carried the Go names, so nothing at runtime changes.

## Verification

- Focused Ginkgo suites for `pkg/api`, `pkg/ai/approval`, `pkg/ai/callertools`, `pkg/ai/provider/...`, `pkg/database`, `pkg/promptrun`.
- A seeded PostgreSQL smoke test: a pre-existing `tool_approval` row without `kind` resolves and decides exactly as before.
- Phase 4 fixture tests for Gavel `v0.0.63` shapes and the old clicky-ui props.
- Gavel API integration tests that persist and resolve each kind, and vitest for the clicky-ui typed controls. Check the visible session flow with agent-browser.
- `make lint` and `make build` in captain and Gavel, and the clicky-ui lint/build gates.

## Out of scope

- Persisting approval decisions as permission-preset updates (gavel TODO `592982ed`).
- Codex execpolicy and network-policy amendments.
