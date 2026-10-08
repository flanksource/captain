---
exec: bash
args: ["-c", "captain permissions matrix {{.flags}}"]
flags: ""
---

# Permission Capability Matrix

The printed matrix is the contract: it declares what each runtime actually does
with a `permissions` block, so a setting that is silently dropped shows up as a
row rather than as a surprise minutes into a run.

These cases pin the cells that carry a real finding. Changing captain's
behaviour is meant to change a cell here, and that change is meant to be visible
in review.

## Grid shape

| Name | flags | CEL Validation |
|------|-------|----------------|
| groups by agent family | | stdout.contains("claude") && stdout.contains("codex") && stdout.contains("gemini") |
| names every runtime | | stdout.contains("anthropic agent") && stdout.contains("anthropic cli") && stdout.contains("anthropic cmux") && stdout.contains("openai agent") && stdout.contains("openai cli") && stdout.contains("openai cmux") && stdout.contains("google cli") |
| names every API runtime | | stdout.contains("anthropic api") && stdout.contains("openai api") && stdout.contains("google api") && stdout.contains("deepseek api") |
| rows cover every posture | | stdout.contains("mode acceptEdits") && stdout.contains("mode bypassPermissions") && stdout.contains("mode dontAsk") && stdout.contains("mode plan") |
| rows cover both axes | | stdout.contains("tool deny") && stdout.contains("mcp disabled") && stdout.contains("skills enabled") |
| prints the legend | | stdout.contains("approximated") && stdout.contains("approval broker") |

## Postures

| Name | flags | CEL Validation |
|------|-------|----------------|
| claude honours every posture natively | --provider anthropic --mode cli --format json | json.runtimes[0].permissions.modes["plan"].kind == "native" && json.runtimes[0].permissions.modes["dontAsk"].kind == "native" |
| claude-cli omits the flag for the unset posture | --provider anthropic --mode cli --format json | json.runtimes[0].permissions.modes["default"].effects == null |
| claude-agent sends the unset posture explicitly | --provider anthropic --mode agent --format json | json.runtimes[0].permissions.modes["default"].effects.flag == "permissionMode=default" |
| codex approximates plan without changing isolation | --provider openai --mode cli --format json | json.runtimes[0].permissions.modes["plan"].kind == "approximated" && json.runtimes[0].permissions.modes["plan"].effects.approval == "on-request" && !has(json.runtimes[0].permissions.modes["plan"].effects.sandbox) |
| codex auto routes approvals to its auto-review subagent | --provider openai --mode agent --format json | json.runtimes[0].permissions.modes["auto"].kind == "native" && json.runtimes[0].permissions.modes["auto"].effects.reviewer == "auto_review" |
| codex cannot express dontAsk | --provider openai --mode cli --format json | json.runtimes[0].permissions.modes["dontAsk"].kind == "unsupported" |
| every unsupported posture says why | --provider openai --mode cli --format json | json.runtimes[0].permissions.modes["dontAsk"].effects.note != "" |
| API backends honour no posture at all | --provider anthropic --mode api --format json | json.runtimes[0].permissions.modes.all(m, json.runtimes[0].permissions.modes[m].kind == "unsupported") |
| gemini bypass maps exactly to yolo | --provider google --mode cli --format json | json.runtimes[0].permissions.modes["bypassPermissions"].effects.flag == "--approval-mode yolo" |

## Tool policy by provenance

The same policy on the same runtime has two different answers depending on where
the tool came from. codex has no tool filter of its own, but captain builds the
caller-tool list itself and simply omits a denied tool — so `deny` is enforced
there while `deny` on a codex built-in is not.

| Name | flags | CEL Validation |
|------|-------|----------------|
| codex-agent cannot filter its own built-ins | --provider openai --mode agent --format json | json.runtimes[0].permissions.toolPolicies.agent["deny"].kind == "unsupported" |
| codex-agent enforces deny on a caller tool | --provider openai --mode agent --format json | json.runtimes[0].permissions.toolPolicies.caller["deny"].kind == "native" |
| claude-cli filters its built-ins | --provider anthropic --mode cli --format json | json.runtimes[0].permissions.toolPolicies.agent["deny"].kind == "native" |
| claude-cli serves no caller tools | --provider anthropic --mode cli --format json | json.runtimes[0].permissions.toolPolicies.caller["deny"].kind == "unsupported" |
| allow is an auto-approve list, not a restriction | --provider anthropic --mode cli --format json | json.runtimes[0].permissions.toolPolicies.agent["allow"].effects.note.contains("auto-approve") |
| ask on a caller tool needs a broker | --provider anthropic --mode agent --format json | json.runtimes[0].permissions.toolPolicies.caller["ask"].kind == "requires-broker" |
| no per-tool policy over a third-party MCP server | --provider anthropic --mode agent --format json | json.runtimes[0].permissions.toolPolicies.mcp["deny"].kind == "unsupported" |
| auto constrains nothing anywhere | --provider deepseek --mode api --format json | json.runtimes[0].permissions.toolPolicies.agent["auto"].kind == "native" |
| the grid shows the selected provenance | --provenance caller | stdout.contains("caller") |

## Resources

MCP is one-directional today: it can only be switched off. Skills are enabled only on claude-cli, and a disabled skill is dropped for every runtime before any provider sees it. `plugins` does nothing at all.

| Name | flags | CEL Validation |
|------|-------|----------------|
| claude-cli silences ambient MCP | --provider anthropic --mode cli --format json | json.runtimes[0].permissions.resources.mcp["disabled"].kind == "native" |
| codex-agent silences ambient MCP | --provider openai --mode agent --format json | json.runtimes[0].permissions.resources.mcp["disabled"].kind == "native" |
| claude-agent silences ambient MCP | --provider anthropic --mode agent --format json | json.runtimes[0].permissions.resources.mcp["disabled"].kind == "native" |
| no backend enables MCP per server | --provider anthropic --mode cli --format json | json.runtimes[0].permissions.resources.mcp["enabled"].kind == "unsupported" |
| only claude-cli loads skills | --provider anthropic --mode cli --format json | json.runtimes[0].permissions.resources.skills["enabled"].kind == "native" |
| every runtime drops a disabled skill | --format json | json.runtimes.all(r, r.permissions.resources.skills["disabled"].kind == "native") |
| plugins are inert in both directions | --provider anthropic --mode cli --format json | json.runtimes[0].permissions.resources.plugins["enabled"].kind == "unsupported" && json.runtimes[0].permissions.resources.plugins["disabled"].kind == "unsupported" |

## Built-in tool vocabulary

The permission catalog served Claude's tool names for every runtime. codex has
never had a tool called Bash.

| Name | flags | CEL Validation |
|------|-------|----------------|
| claude names its own tools | --provider anthropic --mode cli --format json | json.runtimes[0].permissions.tools.exists(t, t == "Bash") && json.runtimes[0].permissions.tools.exists(t, t == "WebFetch") |
| codex names its own tools | --provider openai --mode cli --format json | json.runtimes[0].permissions.tools.exists(t, t == "shell") && json.runtimes[0].permissions.tools.exists(t, t == "apply_patch") |
| codex has no Bash | --provider openai --mode cli --format json | !json.runtimes[0].permissions.tools.exists(t, t == "Bash") |
| gemini names its own tools | --provider google --mode cli --format json | json.runtimes[0].permissions.tools.exists(t, t == "run_shell_command") |
| API backends have no built-ins | --provider anthropic --mode api --format json | !has(json.runtimes[0].permissions.tools) |

## Caveats

| Name | flags | CEL Validation |
|------|-------|----------------|
| notes are off by default | --provider openai --mode cli --format json | !has(json.notes) |
| notes explain why codex refuses dontAsk | --provider openai --mode cli --notes --format json | json.notes.exists(n, n.setting == "mode dontAsk" && n.support == "unsupported" && n.note.contains("tool-level denial")) |
| notes explain the codex plan approximation | --provider openai --mode agent --notes --format json | json.notes.exists(n, n.setting == "mode plan" && n.support == "approximated") |
| notes are sorted for a stable diff | --notes --format json | json.notes.size() > 0 |
| the pretty form prints the caveat table | --provider openai --mode cli --notes | stdout.contains("Caveats") |

## Selectors

| Name | flags | CEL Validation |
|------|-------|----------------|
| default covers all eleven runtimes | --format json | json.runtimes.size() == 11 |
| provider narrows to its modes | --provider openai --format json | json.runtimes.size() == 4 && json.runtimes.all(r, r.provider == "openai") |
| provider and mode narrow to one | --provider openai --mode agent --format json | json.runtimes.size() == 1 && json.runtimes[0].provider == "openai" && json.runtimes[0].mode == "agent" |
| an agent family name selects its provider | --provider codex --mode agent --format json | json.runtimes.size() == 1 && json.runtimes[0].provider == "openai" |

A mistyped selector fails loud rather than quietly printing the full matrix or an
empty one — the same rule the declaration itself follows.

| Name | flags | Exit Code | CEL Validation |
|------|-------|-----------|----------------|
| an unknown provider is refused | --provider claude-cli | 1 | stderr.contains("unknown provider") && stderr.contains("anthropic") |
| an unknown mode is refused | --mode headless | 1 | stderr.contains("unknown mode") && stderr.contains("agent") |
| a provider without that mode is refused | --provider google --mode agent | 1 | stderr.contains("no runtime matches") && stderr.contains("google: api, cli") |
| an unknown provenance is refused | --provenance builtin | 1 | stderr.contains("unknown tool provenance") && stderr.contains("caller") |
