---
exec: bash
args: ["-c", "captain permissions matrix {{.flags}}"]
flags: ""
---

# Permission Capability Matrix

The printed matrix is the contract: it declares what each backend actually does
with a `permissions` block, so a setting that is silently dropped shows up as a
row rather than as a surprise minutes into a run.

These cases pin the cells that carry a real finding. Changing captain's
behaviour is meant to change a cell here, and that change is meant to be visible
in review.

## Grid shape

| Name | flags | CEL Validation |
|------|-------|----------------|
| groups by agent family | | stdout.contains("claude") && stdout.contains("codex") && stdout.contains("gemini") |
| names every backend | | stdout.contains("claude-cli") && stdout.contains("claude-agent") && stdout.contains("claude-cmux") && stdout.contains("codex-cli") && stdout.contains("codex-agent") && stdout.contains("codex-cmux") && stdout.contains("gemini-cli") |
| names every API backend | | stdout.contains("anthropic") && stdout.contains("openai") && stdout.contains("deepseek") |
| rows cover every posture | | stdout.contains("mode acceptEdits") && stdout.contains("mode bypassPermissions") && stdout.contains("mode dontAsk") && stdout.contains("mode plan") |
| rows cover both axes | | stdout.contains("tool deny") && stdout.contains("mcp disabled") && stdout.contains("skills enabled") |
| prints the legend | | stdout.contains("approximated") && stdout.contains("approval broker") |

## Postures

| Name | flags | CEL Validation |
|------|-------|----------------|
| claude honours every posture natively | --backend claude-cli --format json | json.backends[0].permissions.modes["plan"].kind == "native" && json.backends[0].permissions.modes["dontAsk"].kind == "native" |
| claude-cli omits the flag for the unset posture | --backend claude-cli --format json | json.backends[0].permissions.modes["default"].effects == null |
| claude-agent sends the unset posture explicitly | --backend claude-agent --format json | json.backends[0].permissions.modes["default"].effects.flag == "permissionMode=default" |
| codex approximates every posture it honours | --backend codex-cli --format json | json.backends[0].permissions.modes["plan"].kind == "approximated" && json.backends[0].permissions.modes["plan"].effects.sandbox == "read-only" |
| codex cannot express dontAsk | --backend codex-cli --format json | json.backends[0].permissions.modes["dontAsk"].kind == "unsupported" |
| every unsupported posture says why | --backend codex-cli --format json | json.backends[0].permissions.modes["dontAsk"].effects.note != "" |
| API backends honour no posture at all | --backend anthropic --format json | json.backends[0].permissions.modes.all(m, json.backends[0].permissions.modes[m].kind == "unsupported") |
| gemini bypass maps exactly to yolo | --backend gemini-cli --format json | json.backends[0].permissions.modes["bypassPermissions"].effects.flag == "--approval-mode yolo" |

## Tool policy by provenance

The same policy on the same backend has two different answers depending on where
the tool came from. codex has no tool filter of its own, but captain builds the
caller-tool list itself and simply omits a denied tool — so `deny` is enforced
there while `deny` on a codex built-in is not.

| Name | flags | CEL Validation |
|------|-------|----------------|
| codex-agent cannot filter its own built-ins | --backend codex-agent --format json | json.backends[0].permissions.toolPolicies.agent["deny"].kind == "unsupported" |
| codex-agent enforces deny on a caller tool | --backend codex-agent --format json | json.backends[0].permissions.toolPolicies.caller["deny"].kind == "native" |
| claude-cli filters its built-ins | --backend claude-cli --format json | json.backends[0].permissions.toolPolicies.agent["deny"].kind == "native" |
| claude-cli serves no caller tools | --backend claude-cli --format json | json.backends[0].permissions.toolPolicies.caller["deny"].kind == "unsupported" |
| allow is an auto-approve list, not a restriction | --backend claude-cli --format json | json.backends[0].permissions.toolPolicies.agent["allow"].effects.note.contains("auto-approve") |
| ask on a caller tool needs a broker | --backend claude-agent --format json | json.backends[0].permissions.toolPolicies.caller["ask"].kind == "requires-broker" |
| no per-tool policy over a third-party MCP server | --backend claude-agent --format json | json.backends[0].permissions.toolPolicies.mcp["deny"].kind == "unsupported" |
| auto constrains nothing anywhere | --backend deepseek --format json | json.backends[0].permissions.toolPolicies.agent["auto"].kind == "native" |
| the grid shows the selected provenance | --provenance caller | stdout.contains("caller") |

## Resources

Both resource kinds are one-directional today, in opposite directions: MCP can
only be switched off, skills can only be switched on, and `plugins` does nothing
at all.

| Name | flags | CEL Validation |
|------|-------|----------------|
| claude-cli silences ambient MCP | --backend claude-cli --format json | json.backends[0].permissions.resources.mcp["disabled"].kind == "native" |
| codex-agent silences ambient MCP | --backend codex-agent --format json | json.backends[0].permissions.resources.mcp["disabled"].kind == "native" |
| claude-agent accepts and drops it | --backend claude-agent --format json | json.backends[0].permissions.resources.mcp["disabled"].kind == "unsupported" |
| no backend enables MCP per server | --backend claude-cli --format json | json.backends[0].permissions.resources.mcp["enabled"].kind == "unsupported" |
| only claude-cli loads skills | --backend claude-cli --format json | json.backends[0].permissions.resources.skills["enabled"].kind == "native" |
| nothing unloads a skill | --backend claude-cli --format json | json.backends[0].permissions.resources.skills["disabled"].kind == "unsupported" |
| plugins are inert in both directions | --backend claude-cli --format json | json.backends[0].permissions.resources.plugins["enabled"].kind == "unsupported" && json.backends[0].permissions.resources.plugins["disabled"].kind == "unsupported" |

## Built-in tool vocabulary

The permission catalog served Claude's tool names for every backend. codex has
never had a tool called Bash.

| Name | flags | CEL Validation |
|------|-------|----------------|
| claude names its own tools | --backend claude-cli --format json | json.backends[0].permissions.tools.exists(t, t == "Bash") && json.backends[0].permissions.tools.exists(t, t == "WebFetch") |
| codex names its own tools | --backend codex-cli --format json | json.backends[0].permissions.tools.exists(t, t == "shell") && json.backends[0].permissions.tools.exists(t, t == "apply_patch") |
| codex has no Bash | --backend codex-cli --format json | !json.backends[0].permissions.tools.exists(t, t == "Bash") |
| gemini names its own tools | --backend gemini-cli --format json | json.backends[0].permissions.tools.exists(t, t == "run_shell_command") |
| API backends have no built-ins | --backend anthropic --format json | !has(json.backends[0].permissions.tools) |

## Caveats

| Name | flags | CEL Validation |
|------|-------|----------------|
| notes are off by default | --backend codex-cli --format json | !has(json.notes) |
| notes explain the dontAsk inversion | --backend codex-cli --notes --format json | json.notes.exists(n, n.setting == "mode dontAsk" && n.support == "unsupported" && n.note.contains("read-only")) |
| notes explain the codex plan approximation | --backend codex-agent --notes --format json | json.notes.exists(n, n.setting == "mode plan" && n.support == "approximated") |
| notes are sorted for a stable diff | --notes --format json | json.notes.size() > 0 |
| the pretty form prints the caveat table | --backend codex-cli --notes | stdout.contains("Caveats") |

## Selectors

| Name | flags | CEL Validation |
|------|-------|----------------|
| default covers all eleven backends | --format json | json.backends.size() == 11 |
| backend narrows to one | --backend codex-agent --format json | json.backends.size() == 1 && json.backends[0].backend == "codex-agent" |

A mistyped selector fails loud rather than quietly printing the full matrix or an
empty one — the same rule the declaration itself follows.

| Name | flags | Exit Code | CEL Validation |
|------|-------|-----------|----------------|
| a family name is not a backend | --backend claude | 1 | stderr.contains("unknown backend") && stderr.contains("claude-cli") |
| an unknown provenance is refused | --provenance builtin | 1 | stderr.contains("unknown tool provenance") && stderr.contains("caller") |
