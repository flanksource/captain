# Unified approval callback: request examples

This is a companion to [unified-approval-callback.md](unified-approval-callback.md). It shows how each approval kind reaches `Config.OnApproval`:

- the native request as it arrives
- the extended `api.ApprovalRequest` Captain builds from it
- an example `api.ApprovalDecision`
- the native answer that decision becomes

JSON keys are the persisted `captain_turn_requests.request` / `response` shape. Fields that exist today (`tool`, `input`, `toolUseId`, `allow`, `message`, `updatedInput`) are unchanged. Everything else is additive.

## Where the examples come from

| Source | What it holds | Counts |
| --- | --- | --- |
| Captain `captain_turn_requests` (local Gavel DB) | Claude Agent requests brokered through `OnApproval`, raised by `gavel-dashboard` and `caller_tool` | 23 rows: Edit 13, Write 3, AskUserQuestion 3, Read 2, Monitor 1, `whoami` 1. 14 approved, 8 expired, 1 cancelled. |
| Codex rollouts (`~/.codex/sessions/2026/**`), the `>>> APPROVAL REQUEST` blocks Codex's auto-reviewer receives | The approval request Codex raises before it reaches a client | 16,865 unique: `exec_command` 14,109, `apply_patch` 2,083, `mcp_tool_call` 671, `write_stdin` 2. No network, permissions, or elicitation requests. |
| Codex rollouts, `request_user_input` function calls | Codex clarification questions | 464 calls |
| Claude transcripts (`~/.claude/projects/**`) | `tool_use` inputs, which are exactly what the SDK passes to `canUseTool` | Bash (including `dangerouslyDisableSandbox`) and WebFetch samples |
| Codex app-server schema (codex-cli 0.157.1) | Wire shapes for request kinds the history never produced | Used for the network, permissions, and elicitation examples |

Every example is labelled **mined** or **schema-derived**. Ids are shortened, long inputs are cut with `…`, and absolute paths are made repo-relative.

The Codex rollouts record Codex's internal request (`justification`, `prefix_rule`, `sandbox_permissions`), not the app-server JSON-RPC params. Each Codex example therefore shows the app-server params the same request produces:

- `justification` → `reason`
- `prefix_rule` → `proposedExecpolicyAmendment`
- the thread, turn, item, and approval ids are illustrative

## Findings from mining

- **G19: the Codex permissions and elicitation kinds will never fire as Captain configures Codex today.** Codex's `approvalPolicy` accepts a `granular` object: `{sandbox_approval, rules, mcp_elicitations, request_permissions (default false), skill_approval}`. Captain only sends the string form (`codex_appserver_protocol.go:416,508`). None of the 16,865 mined requests was a permissions request or an elicitation.
- **G20: `item/fileChange/requestApproval` does not carry the diff.** Its params are only `{threadId, turnId, itemId, reason, grantRoot, startedAtMs}`. The files and patch arrive on the preceding `item/started` fileChange item, so the provider must join the two before calling the callback.
- **Claude requests that ended without an answer.** 8 of 23 brokered Claude requests expired unanswered, with reason `approval timed out`. One caller-tool request was cancelled because its run had already finished. Timeouts and run-end cancellation are the common outcome, not the rare one.
- **Codex's command approval schema has more fields than the plan uses.** It also documents `availableDecisions` and the experimental `additionalPermissions`. When `availableDecisions` is present it drives `interruptible` and `supportedScopes`.

---

## `command`

### Claude Bash with a sandbox escape (mined, Claude transcript)

Native `can_use_tool` params:

```json
{"tool": "Bash", "tool_use_id": "toolu_…", "input": {"command": "gavel pr status 85 2>&1 | tail -60", "dangerouslyDisableSandbox": true}}
```

`ApprovalRequest`:

```json
{
  "tool": "Bash", "input": {"command": "gavel pr status 85 2>&1 | tail -60", "dangerouslyDisableSandbox": true},
  "toolUseId": "toolu_…", "sessionId": "…",
  "kind": "command", "escalates": true, "interruptible": true, "supportedScopes": ["request"],
  "command": {"command": "gavel pr status 85 2>&1 | tail -60", "unsandboxed": true}
}
```

| Decision | Native answer |
| --- | --- |
| `{"allow": true}` | `{"allow": true, "updatedInput": <input>}` |
| `{"allow": false, "interrupt": true, "message": "no network from this run"}` | `{"allow": false, "message": "no network from this run", "interrupt": true}` |

### Codex command escalation with a proposed amendment (mined, Codex approval request)

Codex request:

```json
{"tool": "exec_command", "command": "GIT_EDITOR=true git rebase --continue", "sandbox_permissions": "require_escalated",
 "justification": "May I continue the active rebase, which must update Git metadata in the parent repository's .git directory outside this worktree?",
 "prefix_rule": ["git", "rebase"]}
```

App-server `item/commandExecution/requestApproval` params:

```json
{"threadId": "thr_…", "turnId": "turn_…", "itemId": "item_…", "approvalId": "appr_…", "kind": "command",
 "command": "GIT_EDITOR=true git rebase --continue", "cwd": "…/.shell/worktrees/gavel-pr-110-fix-…",
 "reason": "May I continue the active rebase, …", "proposedExecpolicyAmendment": ["git", "rebase"]}
```

`ApprovalRequest`:

```json
{
  "tool": "exec_command", "input": {…native params…}, "toolUseId": "appr_…",
  "kind": "command", "reason": "May I continue the active rebase, …",
  "escalates": true, "interruptible": true, "supportedScopes": ["request", "session"],
  "command": {"command": "GIT_EDITOR=true git rebase --continue", "cwd": "…", "proposedPolicy": ["git", "rebase"]}
}
```

`proposedPolicy` is shown to the host but never offered; see Decisions in the plan. The `toolUseId` is `approvalId`, not `itemId` (G8). The decision translates as follows:

| Decision | Native answer |
| --- | --- |
| `{"allow": true}` | `{"decision": "accept"}` |
| `{"allow": true, "scope": "session"}` | `{"decision": "acceptForSession"}` |
| `{"allow": false}` | `{"decision": "decline"}` |
| `{"allow": false, "interrupt": true}` | `{"decision": "cancel"}` |

### Codex write to a running terminal (mined, Codex approval request)

Codex request:

```json
{"tool": "write_stdin", "session_id": 17473, "chars": "n\n", "tty": true, "sandbox_permissions": "require_escalated", "cwd": "…/gavel"}
```

App-server params carry `"kind": "writeStdin"`. This is input to a process that is already running, not a new command:

```json
{"tool": "write_stdin", "toolUseId": "appr_…", "kind": "command", "escalates": true, "interruptible": true,
 "command": {"stdin": {"terminal": "17473", "chars": "n\n"}}}
```

---

## `tool`

### Captain caller tool (mined, `captain_turn_requests`, `requested_by: caller_tool`)

The stored row is already a valid extended request. `callertools.Runtime` adds only `kind` (G3, G16):

```json
{"tool": "whoami", "input": {"limit": 5, "provider": "google"}, "toolUseId": "toolu_011Go7…",
 "kind": "tool", "interruptible": false, "supportedScopes": ["request"]}
```

The recorded outcome was `cancelled`, reason `the prompt run is succeeded, so the "whoami" approval can no longer be answered`. The callback's context was cancelled, so this is a lifecycle end, not a deny.

### Codex MCP connector tool (mined, Codex approval request)

```json
{"tool": "mcp_tool_call", "server": "playwright", "tool_name": "browser_tabs", "arguments": {"action": "close", "index": 0},
 "tool_description": "List, create, close, or select a browser tab.",
 "annotations": {"destructive_hint": true, "open_world_hint": true, "read_only_hint": false}}
```

```json
{"tool": "mcp__playwright__browser_tabs", "input": {"action": "close", "index": 0}, "toolUseId": "…",
 "kind": "tool", "interruptible": true}
```

`tool` follows Claude's `mcp__<server>__<tool>` naming, so one `PermissionRule` matches the same MCP tool on both providers. `Info` is not persisted. It carries `Parent: "playwright"` and `DestructiveHint: true`, so the deny pre-check can match hint facets before the callback runs: a playwright rule of `destructive: true → deny` never reaches the host.

### Claude built-in tool with no narrower kind (mined, `captain_turn_requests`)

```json
{"tool": "Monitor", "input": {"command": "prev=$(stat -f \"%m\" …)\nquiet=0\nwhile [ $quiet -lt 5 ]; do …", "persistent": false, "timeout_ms": 3…},
 "toolUseId": "toolu_01XoN7…", "kind": "tool", "interruptible": true}
```

`Monitor` runs a shell loop but is not `Bash`, so mapping stays conservative: `tool` with the raw input. It was approved with `{"allow": true}`.

---

## `filesystem`

### Claude Edit (mined, `captain_turn_requests`, approved)

```json
{"tool": "Edit", "toolUseId": "toolu_01BLB9…",
 "input": {"file_path": "…/pkg/api/permission_capabilities.go", "old_string": "\t// Tools is the …",
           "new_string": "\t// Tools is the runtime's built-in tool vocabulary — …\n\tCallerTools CallerToolTransport `json:\"callerTools,omitempty\"`\n}"},
 "kind": "filesystem", "interruptible": true, "supportedScopes": ["request"],
 "filesystem": {"operation": "edit", "paths": ["…/pkg/api/permission_capabilities.go"]}}
```

On Claude, `filesystem` is an operation approval, not a grant. `{"allow": true}` may carry `updatedInput`, and `grants` is rejected. The same shape covers Write (3 rows) and Read (2 rows, `operation: read`). The expired Write row shows the other outcome: the broker expiry ended the callback's context, with reason `approval timed out`.

### Codex file change (mined, Codex approval request)

The Codex request, which the auto-reviewer sees in full:

```json
{"tool": "apply_patch", "cwd": "…/incident-commander", "files": ["…/facet/src/components/ListTable.tsx"], "change_count": 1,
 "patch": "*** Begin Patch\n*** Delete File: …/facet/src/components/ListTable.tsx\n*** End Patch"}
```

App-server `item/fileChange/requestApproval` params carry none of that (G20):

```json
{"threadId": "thr_…", "turnId": "turn_…", "itemId": "item_…", "reason": null, "grantRoot": null}
```

After joining with the `item/started` fileChange item:

```json
{"tool": "apply_patch", "input": {…joined item…}, "toolUseId": "item_…",
 "kind": "filesystem", "escalates": true, "interruptible": true, "supportedScopes": ["request", "session"],
 "filesystem": {"operation": "patch", "paths": ["…/facet/src/components/ListTable.tsx"], "changes": [{"path": "…", "kind": "delete"}]}}
```

`escalates` is set because the patch writes outside `cwd`, into the sibling `facet` repo. `{"allow": true, "scope": "session"}` → `{"decision": "acceptForSession"}`.

---

## `network`

### Claude WebFetch (mined, Claude transcript)

```json
{"tool": "WebFetch", "input": {"url": "https://code.claude.com/docs/en/plugins.md", "prompt": "Plugin structure, directory layout, …"},
 "toolUseId": "toolu_…", "kind": "network", "interruptible": true, "supportedScopes": ["request"],
 "network": {"host": "code.claude.com", "protocol": "https", "url": "https://code.claude.com/docs/en/plugins.md"}}
```

### Codex command that needs network access (schema-derived from a mined command)

The mined request justifies itself as needing GitHub access, but it ran with network enabled, so no network approval was raised:

```json
{"command": ["/bin/zsh", "-lc", "./.bin/gavel serve flanksource/gavel --addr 127.0.0.1 --port 9092 --any --status -v"],
 "justification": "Allow starting the local PR dashboard, opening its local URL, and fetching read-only GitHub PR/check data for the requested smoke test?"}
```

Under a network-restricted sandbox, the same command raises:

```json
{"threadId": "thr_…", "turnId": "turn_…", "itemId": "item_…", "approvalId": "appr_…", "kind": "command",
 "command": "./.bin/gavel serve flanksource/gavel …", "networkApprovalContext": {"host": "api.github.com", "protocol": "https"},
 "proposedNetworkPolicyAmendments": [{"host": "api.github.com", "action": "allow"}]}
```

```json
{"tool": "exec_command", "input": {…}, "toolUseId": "appr_…",
 "kind": "network", "escalates": true, "interruptible": true, "supportedScopes": ["request", "session"],
 "network": {"host": "api.github.com", "protocol": "https", "command": "./.bin/gavel serve flanksource/gavel …"}}
```

The command context is kept, and the amendment proposals are for display only. `{"allow": true}` → `accept`, and adding `"scope": "session"` → `acceptForSession`.

---

## `permissions` (schema-derived; no occurrence in 16,865 mined requests, see G19)

This example is motivated by the mined rebase request above. With `request_permissions` enabled, Codex asks for the grant up front instead of escalating each command:

```json
{"threadId": "thr_…", "turnId": "turn_…", "itemId": "item_…", "cwd": "…/.shell/worktrees/gavel-pr-110-fix-…",
 "reason": "Rebase must update Git metadata in the parent repository's .git directory",
 "permissions": {"fileSystem": {"entries": [{"path": {"type": "path", "path": "…/gavel/.git"}, "access": "write"}]},
                 "network": {"enabled": true}}}
```

The grant is expressed in the spec's own sandbox vocabulary (`api.NativeSandboxPolicy`):

```json
{"tool": "request_permissions", "input": {…}, "toolUseId": "item_…",
 "kind": "permissions", "reason": "Rebase must update Git metadata …", "escalates": true, "interruptible": false, "supportedScopes": ["turn", "session"],
 "permissions": {"filesystem": {"writableRoots": ["…/gavel/.git"]}, "network": {"access": "unrestricted"}}}
```

Decision granting a subset:

```json
{"allow": true, "scope": "turn", "grants": {"filesystem": {"writableRoots": ["…/gavel/.git"]}}}
```

That becomes `{"permissions": {"fileSystem": {"entries": [{"path": {"type": "path", "path": "…/gavel/.git"}, "access": "write"}]}}, "scope": "turn"}`: network is not granted. `{"allow": true}` with no `grants` grants both. `Validate` rejects a root Codex did not request, and a root that overlaps a deny rule, for example `permissions.filesystem` denying `**/.git`.

---

## `question`

### Claude AskUserQuestion (mined, `captain_turn_requests`, approved with answers)

```json
{"tool": "AskUserQuestion", "toolUseId": "toolu_01RVJL…",
 "input": {"questions": [
   {"header": "Presets", "question": "The todo says \"move permission mode and preset selection as runtime bar fields\". Which \"preset selection\" do you mean?",
    "options": [{"label": "Runtime presets (Recommended)", "description": "…", "preview": "…"}, {"label": "Permission presets", "…": "…"}]},
   {"question": "What should \"overflowing to 3dot menu\" mean concretely for the new permission-mode and preset fields?", "…": "…"}]},
 "kind": "question", "interruptible": true,
 "questions": [{"text": "The todo says …", "options": ["Runtime presets (Recommended)", "Permission presets"], "optionDescriptions": {…}},
               {"text": "What should \"overflowing to 3dot menu\" …", "options": […]}]}
```

The recorded decision already uses the existing answer convention, and it is unchanged:

```json
{"allow": true, "updatedInput": {"answers": {"The todo says …": "Runtime presets (Recommended)", "What should \"overflowing to 3dot menu\" …": "CSS breakpoint (Recommended)"}, "questions": […]}}
```

Claude keys answers by question text, because `TerminalQuestion.ID` is empty for Claude. Two other AskUserQuestion rows expired unanswered.

### Codex request_user_input (mined, Codex rollout)

```json
{"questions": [{"id": "data_model", "header": "Data Model",
  "question": "How should the new rate-table model relate to the existing `forex_rates` table?",
  "options": [{"label": "Generic Plus Bridge (Recommended)", "description": "…"}, {"label": "Replace Forex", "…": "…"}, {"label": "Forex Separate", "…": "…"}]},
 {"id": "rate_inputs", "header": "Rate Inputs", "question": "What should be the primary v1 source for generated rate tables?", "…": "…"}]}
```

`tool` stays `AskUserQuestion` and `toolUseId` stays the `itemId`, exactly as today:

```json
{"tool": "AskUserQuestion", "input": {…native params…}, "toolUseId": "item_…", "kind": "question", "interruptible": false,
 "questions": [{"id": "data_model", "text": "How should …", "options": ["Generic Plus Bridge (Recommended)", "Replace Forex", "Forex Separate"], "multiSelect": true, "secret": false}, …]}
```

Decision `{"allow": true, "updatedInput": {"answers": {"data_model": ["Generic Plus Bridge (Recommended)"]}}}` → `{"answers": {"data_model": {"answers": ["Generic Plus Bridge (Recommended)"]}}}`. Codex questions cannot be declined: `{"allow": false}` returns an RPC error, as today, and `"interrupt": true` fails `Validate` because the request is not `interruptible`.

---

## `elicitation` (schema-derived; no occurrence in Codex or Claude history)

### Form mode (Codex `mcpServer/elicitation/request`; Claude `onElicitation` has the same MCP shape)

```json
{"threadId": "thr_…", "turnId": "turn_…", "serverName": "github", "mode": "form",
 "message": "Which repository should the issue be filed in?",
 "requestedSchema": {"type": "object", "properties": {"repo": {"type": "string", "enum": ["flanksource/captain", "flanksource/gavel"]}, "labels": {"type": "string"}}, "required": ["repo"]}}
```

```json
{"tool": "Elicitation", "input": {…native params…}, "toolUseId": "elicit:<rpc id>",
 "kind": "elicitation", "interruptible": true,
 "elicitation": {"server": "github", "mode": "form", "message": "Which repository …", "schema": {…}}}
```

| Decision | Native answer |
| --- | --- |
| `{"allow": true, "updatedInput": {"repo": "flanksource/captain"}}` | `{"action": "accept", "content": {"repo": "flanksource/captain"}}` |
| `{"allow": false}` | `{"action": "decline"}` |
| `{"allow": false, "interrupt": true}` | `{"action": "cancel"}` |

`{"allow": true, "updatedInput": {"labels": "bug"}}` fails `Validate`, because the required `repo` is missing.

### URL mode

```json
{"serverName": "linear", "mode": "url", "elicitationId": "el_…", "message": "Authorize access to your workspace", "url": "https://linear.example/oauth/authorize?…"}
```

```json
{"tool": "Elicitation", "input": {…}, "toolUseId": "elicit:<rpc id>", "kind": "elicitation", "interruptible": true,
 "elicitation": {"server": "linear", "mode": "url", "elicitationId": "el_…", "message": "Authorize access to your workspace", "url": "https://linear.example/oauth/authorize?…"}}
```

`{"allow": true}` with no `updatedInput` means the user finished the flow at the URL, and becomes `{"action": "accept"}`. Content on a url-mode decision fails `Validate`.
