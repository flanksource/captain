# Captain

Captain is a Go CLI for working with **Claude Code sessions, hooks, and sandboxes**.

It provides tools to:

- inspect Claude Code project/session history
- summarize tool usage, paths, binaries, and API cost
- list files changed by a session
- install Claude Code hooks
- enforce a session-specific **Definition of Done** gate
- test and use AI providers from the command line
- run iterative AI agents with verifiers, worktrees, and commits
- generate/build/run containerized Claude Code sandboxes
- inspect and clean stored Claude project session data
- expose captain commands as an MCP server
- run a web UI for launching and chatting with AI agents
- configure default backend, model, and AI safety toggles

```xml h=185px w=358px preview
<div style="font-family:system-ui,sans-serif;padding:20px;color:var(--foreground)">
  <label for="amt" style="font-size:14px;font-weight:600">Monthly budget</label>
  <div id="out" style="font-size:30px;font-weight:700;color:var(--chart-1);margin:6px 0">$2,500</div>
  <input id="amt" type="range" min="500" max="10000" step="100" value="2500"
    style="width:100%;accent-color:var(--primary)" />
  <p style="font-size:13px;color:var(--muted-foreground)">Drag to adjust — the figure updates live.</p>
  <script>
    var amt = document.getElementById('amt');
    var out = document.getElementById('out');
    amt.addEventListener('input', function () {
      out.textContent = '$' + Number(amt.value).toLocaleString();
    });
  </script>
</div>
```

```html preview
<div style="font-family:system-ui,sans-serif;padding:20px">
  <div id="cards" style="display:flex;gap:14px;flex-wrap:wrap"></div>
  <script>
    var stats = [
      ['Active users', '12,480', '+8.2% MoM', 'var(--chart-2)'],
      ['Revenue', '$48.2k', '+3.1% MoM', 'var(--chart-1)'],
      ['Churn', '2.4%', '-0.5% MoM', 'var(--chart-5)']
    ];
    document.getElementById('cards').innerHTML = stats.map(function (s) {
      return '<div style="flex:1;min-width:150px;padding:16px;background:var(--card);' +
        'color:var(--card-foreground);border:1px solid var(--border);' +
        'border-radius:var(--radius)">' +
        '<div style="font-size:13px;color:var(--muted-foreground)">' + s[0] + '</div>' +
        '<div style="font-size:26px;font-weight:700;margin-top:4px">' + s[1] + '</div>' +
        '<div style="font-size:12px;font-weight:600;margin-top:4px;color:' + s[3] + '">' +
        s[2] + '</div>' +
        '</div>';
    }).join('');
  </script>
</div>
```

## What it does

From the codebase, Captain is organized around a few core capabilities:

### 1. Claude Code history and cost analysis

Captain reads Claude session history and exposes commands for:

- `history` — inspect tool usage from Claude sessions
- `info` — show project/session metadata for the current directory
- `cost` — estimate and group token/API usage across sessions
- `projects list` — list tracked Claude projects and sessions
- `projects clean` — remove old session history

It analyzes:

- tools used
- files/directories read and written
- domains accessed
- binaries executed
- approval / denial state
- categories of bash activity
- token and cost summaries

### 2. Claude hooks

Captain can install hook commands into Claude settings for:

- **PreToolUse bash scanning** via `hook bash-check`
- **Stop hook gating** via `hook dod install`

The bash-check hook scans bash commands and can deny unsafe or disallowed commands.

### 3. Definition of Done (DoD)

Captain supports a per-session Definition of Done workflow:

- `dod set` — attach one or more validation commands to a Claude session
- `dod check` — intended for Claude Stop hooks
- `dod run` — manually execute DoD checks
- `dod status` — show current DoD config/results
- `dod clear` — remove the DoD gate

This lets Claude continue iterating until required checks pass.

### 4. Session changes

`captain changes` lists the files written or edited during a Claude Code or Codex session:

- defaults to the most recent session in the current directory
- `--session-id` targets a specific session by exact or prefix match
- `--all` searches across all projects
- `--claude` / `--codex` filter by session source
- `--agents` (default: true) includes files edited by nested sub-agents
- `--plans` / `--ignored` control whether plan files and git-ignored files are shown

### 5. AI provider utilities

Captain includes provider-agnostic AI utilities under `captain ai`:

- `ai prompt` — send a prompt to a selected backend/model
- `ai agent` — run an iterative agent loop with optional verifiers, a throwaway git worktree, commit, and LLM judge
- `ai models` — list model information
- `ai test` — verify provider connectivity
- `ai fixture` — run a YAML benchmark fixture across multiple Claude configurations and capture a markdown evidence report

Supported backends are inferred from code and dependencies, including:

- Anthropic
- Gemini / Google
- OpenAI-compatible paths via the internal provider layer
- CLI/provider abstractions for local tool-backed backends (claude, codex, gemini)

### 6. Adapter status and configuration

- `whoami` — lists every AI adapter (API providers and CLI agents), how each is authenticated, whether its binary is installed, and the models it exposes
- `configure` — configure per-provider agent, model, and effort defaults in `~/.captain.yaml`, or validate and save direct-provider API tokens in `~/.config/captain/vault`

### 7. Web UI and MCP server

- `serve` — starts an HTTP API and embedded web UI for launching AI agents and opening follow-up chat sessions; supports `--dev` to proxy to the Vite dev server
- `mcp` — exposes captain commands (history, info, cost, changes, dod, etc.) as MCP tools so Claude Code can invoke them directly

### 8. Utility commands

- `cmux info <copy-id-lines|pid>` — resolves all processes in a cmux surface, pane, or workspace and reports runtime, CPU, memory, listening TCP ports, and optional Go stacks
- `cmux screenshot` — captures a screenshot of the active browser surface in cmux and copies the path to the clipboard
- `port kill <port>` — finds and kills the process listening on a TCP port

### 9. Container sandbox builder

Captain can discover Claude-related local configuration and package it into a container sandbox.

Supported workflows include:

- `container` — interactive TUI
- `container list` — list discovered components
- `container generate` — generate Dockerfile/build context and sandbox config
- `container build` — build the sandbox image
- `container run` — run the generated sandbox
- `sandbox presets` — list available sandbox-runtime presets

The container workflow is designed to package things like:

- Claude config
- agents
- commands
- hooks
- MCP server config
- project settings
- token/env passthrough
- sandbox-runtime presets for languages/tools

## Project layout

```text
captain/
├── cmd/captain/           # CLI entrypoint
├── docs/                  # Astro documentation site
├── pkg/ai/                # AI abstraction, provider config, models
├── pkg/ai/agent/          # Iterative agent loop, plugins (verify, worktree, judge)
├── pkg/ai/fixture/        # YAML fixture runner for Claude configuration benchmarks
├── pkg/api/               # HTTP API types and handlers (used by serve)
├── pkg/bash/              # Bash scanning, classification, rules
├── pkg/captainconfig/     # ~/.captain.yaml config load/save
├── pkg/claude/            # Claude history, sessions, parsing, formatting
├── pkg/cli/               # Cobra/clicky command implementations
├── pkg/cli/webapp/        # Embedded React web UI (served by captain serve)
├── pkg/cmux/              # Terminal multiplexer integration (processes and screenshots)
├── pkg/collections/       # Generic collection utilities
├── pkg/container/         # Sandbox discovery, generation, build/run logic
├── pkg/container/base/    # Embedded agent base image (Dockerfile, deps.yaml, entrypoint.sh)
├── pkg/dod/               # Definition of Done persistence and execution
├── pkg/git/               # Git worktree helpers
├── pkg/sandbox/           # Token/preset/sandbox helpers
├── Makefile               # Thin wrapper around Taskfile
└── Taskfile.yaml          # Main developer tasks
```

## CLI overview

Top-level commands exposed by `cmd/captain/main.go`:

```bash
captain history
captain info
captain cost
captain changes
captain sandbox
captain ai
captain whoami
captain configure
captain serve
captain dod
captain hook
captain projects
captain container
captain mcp
captain cmux
captain port
```

### History

```bash
captain history
captain history --summary
captain history --all
captain history --tool Bash --since now-7d
captain history --category git --compact
captain history --session-id 019e0365-dc2a-7ad0-a5a8-78936481a928
captain history 019e0365-dc2a-7ad0-a5a8-78936481a928
```

Useful flags include:

- `--file`
- `--tool`
- `--dir`
- `--category`
- `--approved`
- `--session-id`
- `--limit`
- `--since`
- `--all`
- `--short`
- `--compact`
- `--summary`

### Info

```bash
captain info
captain info --path /path/to/project
```

Shows project root detection, Claude project directory, session counts, history range, and tool call totals.

### Cost

```bash
captain cost
captain cost --group-by project
captain cost --group-by model
captain cost --group-by tool
captain cost --group-by category
captain cost --session-id 019e0365-dc2a-7ad0-a5a8-78936481a928
captain cost --all --since now-30d
```

Supported groupings from the code:

- `session`
- `project`
- `model`
- `day`
- `dir`
- `file`
- `tool`
- `category`

### Changes

```bash
captain changes
captain changes --session-id <session-id>
captain changes --all --since now-7d
captain changes --claude
captain changes --agents=false
```

Lists the files written or edited during a Claude Code or Codex session. Defaults to the most recent session in the current directory.

### Hook installation

Install the bash safety hook:

```bash
captain hook bash-check install
captain hook bash-check install --user
```

Install the DoD stop hook and related skill files:

```bash
captain hook dod install
captain hook dod install --user
```

### Definition of Done

```bash
captain dod set --session-id <session-id> "go test ./..." "golangci-lint run"
captain dod status --session-id <session-id>
captain dod run --session-id <session-id>
captain dod clear --session-id <session-id>
```

### AI utilities

```bash
captain ai prompt --model claude-sonnet-4 --prompt "Summarize this diff"
captain ai agent --prompt "Fix the failing tests" --verify "go test ./..."
captain ai agent --prompt "Refactor this module" --worktree --commit --judge "all tests pass"
captain ai test --model gemini-2.0-flash
captain ai models
captain ai fixture --file examples/ai-fixtures/mission-control-investigate.yaml
```

Relevant provider flags include:

- `--model`
- `--backend`
- `--api-key`
- `--no-cache`
- `--budget`
- `--debug`

#### AI agent

`captain ai agent` runs an iterative AI agent loop with optional quality gates:

- `--prompt` / `-p` — task prompt (required; can be piped from stdin)
- `--system` / `-s` — system prompt override
- `--verify` — shell command run after each turn; non-zero exit triggers a re-run (repeatable)
- `--max-iterations` — max verify-and-rerun iterations (default: 1)
- `--scope` — verifier scope: `changed` (only changed files) or `all` (default)
- `--worktree` — run in a throwaway git branch/worktree
- `--branch` — worktree branch name (default: `captain/agent-<timestamp>`)
- `--commit` — commit changes on the worktree branch (requires `--worktree`)
- `--judge` — LLM rubric; fails a turn when the judge rejects the result

#### Fixture benchmarks

`captain ai fixture` runs the same prompt against multiple Claude
configurations (different models, tool allowlists, MCP servers, prompt
caching on/off) and prints a side-by-side table of duration, cost,
tokens, and tool-call counts. It's intended to produce **evidence** that
one approach — e.g. a structured MCP — is faster and cheaper than a
Bash/CLI equivalent.

```bash
captain ai fixture -f examples/ai-fixtures/mission-control-investigate.yaml
captain ai fixture -f examples/ai-fixtures/mission-control-describe.yaml --report /tmp/mc-describe.md
captain ai fixture -f examples/ai-fixtures/mission-control-multistep.yaml --repeat 5
```

Flags:

- `--file` / `-f` — path to the YAML fixture (required)
- `--report` / `-r` — write an evidence report (headline, metrics table, per-run config, tool-usage breakdown) to this path
- `--format` — report format: `markdown` (default), `html`, or `ansi`
- `--artifacts` — directory for per-run `stream-json` captures (default: `<fixture-dir>/.captain/fixtures/<name>/`)
- `--repeat` — override every run's repeat count (useful for smoke tests: `--repeat 1`)

YAML schema (abridged):

```yaml
name: my-benchmark
description: What you're measuring and why
prompt: |
  The prompt sent to every run (can be overridden per-run).
baseline: direct        # which run to compare against for Speedup/Cheaper ratios
repeat: 3               # default N per run; overridable per-run and via --repeat

defaults:
  timeout: 3m
  permissionMode: bypassPermissions
  promptCaching: true
  model: claude-sonnet-4

runs:
  - name: direct
    tools: [Bash]
    allowedTools: ["Bash(kubectl *)", "Bash(aws *)"]

  - name: mission-control
    tools: [default]
    mcpConfig: [.mcp.json]
    allowedTools: ["mcp__mission-control__*"]
    repeat: 5           # overrides fixture-level repeat for this run
```

#### Isolation guarantees

Two rules the runner enforces for you so direct-vs-MCP comparisons stay
honest — both are automatic, no extra flags needed:

- **MCP is opt-in per run.** A run gets MCP servers only when `mcpConfig`
  is set. With no `mcpConfig`, the runner passes `--strict-mcp-config`
  with an empty inline config, so ambient `.mcp.json` in the fixture
  directory and user-level MCP servers are never picked up.
- **`allowedTools` is treated as a real allowlist.** Claude CLI's
  `--allowedTools` is natively an auto-approve list, not a restriction —
  under `bypassPermissions` the model can still reach for anything. When
  a run specifies `allowedTools`, the runner demotes the effective
  `--permission-mode` from `bypassPermissions` to `default` so unlisted
  tools are denied in non-interactive mode. If you set `permissionMode`
  to anything other than `bypassPermissions` explicitly, your choice is
  preserved. Runs without `allowedTools` keep whatever permission mode
  they asked for.

Practical consequence for a direct-vs-MCP fixture: the `direct` run with
`allowedTools: [Bash(kubectl *), ...]` can only shell out to those
patterns; the `mission-control` run with
`allowedTools: [mcp__mission-control__*]` can only use MCP — Bash is off
even though it's a built-in. Neither run can accidentally borrow from
the other's toolset.

Supported per-run fields: `name`, `prompt`, `system`, `model`, `timeout`,
`cwd`, `permissionMode`, `appendSystemPrompt`, `settings`,
`maxBudgetUSD`, `repeat`, `tools`, `allowedTools`, `disallowedTools`,
`mcpConfig`, `addDir`, `betas`, `extraArgs`, `env`, `promptCaching`,
`noSessionPersistence`, `bare`. See
[`examples/ai-fixtures/`](examples/ai-fixtures/) for working benchmarks.

Repeats (`repeat: N`) execute each run N times and report the mean
duration/cost plus a sample standard deviation — single-shot LLM numbers
are noisy and N≥3 makes comparisons defensible. The raw per-iteration
`stream-json` is saved under the artifacts directory so every claim in
the report is reproducible.

#### Kubernetes proxy capture

Set `captureKubernetesProxy: true` at the fixture level to route every
`kubectl` call made during the fixture through a captain-managed reverse
proxy, and record both layers of activity:

```yaml
captureKubernetesProxy: true
kubeconfig: ~/.kube/config   # optional; defaults to client-go discovery
```

When enabled, the runner:

- starts a localhost reverse proxy that loads the user's kubeconfig (auth
  plugins included) and forwards to the real cluster
- generates a temp kubeconfig pointing at the proxy and injects
  `KUBECONFIG=<that path>` into every run's environment, so kubectl can't
  bypass it
- writes a JSONL log per run/iteration to
  `<artifacts>/<run>-<iter>.kubectl.jsonl` with two record types:
    - `{"type":"command","command":"kubectl get pods -n prod"}` — literal
      CLI invocation parsed from the model's Bash tool calls
    - `{"type":"request","method":"GET","path":"/api/v1/...","status":200}`
      — every API call observed by the proxy
- surfaces a **Kubectl activity** section in the report with per-run CLI
  and API counts plus a few sample commands

### Container sandbox workflow

```bash
captain container
captain container list
captain container generate
captain container generate -i
captain container build --preset golang
captain container run
captain sandbox presets
```

Important generate/build flags:

- `--interactive`
- `--preset`
- `--base`
- `--mode copy|mount`

### Whoami

```bash
captain whoami
captain whoami --backend anthropic
captain whoami --models=false
captain whoami --no-cache
```

Lists every AI adapter (API providers and CLI agents: `anthropic`, `openai`, `gemini`, `claude-cli`, `claude-agent`, `codex-cli`, `gemini-cli`), their authentication method, binary availability, and a live model listing.

Provider model listings are resolved through credential- and endpoint-scoped entries under `~/.config/captain/models/` (24h TTL), and priced from the OpenRouter snapshot in `~/Library/Caches/flanksource/openrouter-pricing.json` (24h TTL). The model cache uses a machine-local keyed namespace, so different provider accounts cannot reuse one another's availability. `--no-cache` skips both caches, re-queries every provider's model endpoint plus OpenRouter pricing, and rewrites only the affected model-cache entries with the fresh result. Add `-v` to see an access line per request, `-vv` for headers and query params, `-vvv` for request bodies, `-vvvv` for response bodies; credentials are redacted at every rung. Failed requests (status >= 400 or a transport error) are logged at the default verbosity. `-Plog.level.http=<level>` raises HTTP logging alone, and `-Phttp.log.base-level=<level>` (or `HTTP_LOG_BASE_LEVEL`) shifts the whole ladder.

To keep the traffic instead of watching it scroll past, `-Phttp.har=<path>` writes every request/response pair — including redirect hops and retries — to a HAR 1.2 archive you can open in browser DevTools. It applies to any command, not just `whoami`, and the file is written even when the command fails. `-Phttp.har.level=metadata` records headers, query strings and timings without bodies; `-Phttp.har.maxBodySize=<bytes>` changes the 64 KB per-body cap (`0` for none). Credentials are masked the same way as in the wire log, which means the archive is safe to attach to a bug report but cannot be replayed — `-Phttp.har.sensitive=true` keeps them verbatim and writes the file `0600`. Use `-Phttp.captain.har=<path>` to capture captain's own traffic when a shared `http.har` is already set.

### Configure

```bash
captain configure
captain configure openai
captain configure openai --test
captain configure openai --agent codex-agent --model gpt-5.6-sol --effort high --active
```

The providerless interactive wizard writes `~/.captain.yaml` with defaults for backend, model, reasoning effort, budget, timeout, and feature toggles (caching, MCP, hooks, skills, user/project settings, memory).

With a provider and any of `--agent`, `--model`, `--effort`, or `--active`, `configure` saves provider-specific runtime defaults in `~/.captain.yaml`. `--effort default` clears an explicit effort so the selected model chooses its own default. The active provider supplies the agent and model for completely flagless runs. Explicit command flags still win, and each fallback model independently inherits defaults from its own provider.

Passing `anthropic`, `openai`, `gemini`, or `deepseek` securely prompts for an API token, validates it with the provider's model-list endpoint, and saves it to `~/.config/captain/vault` only when validation succeeds. `--test` validates the currently effective token without writing. Automation may use `--token`, but interactive input is preferred because command-line arguments can be retained in shell history or process listings. Explicit runtime keys take precedence over the vault, and the vault takes precedence over environment variables.

The `/whoami` page in `captain serve` exposes the same token operations plus agent, model, effort, and active-provider controls. Stored tokens are never displayed; only a masked identifier and credential source are returned.

### Serve

```bash
captain serve
captain serve --port 8080
captain serve --dev
go run ./cmd/captain serve --dev --open
task www:dev
task www:build
```

Starts an HTTP API and embedded web UI. The UI launches `captain ai agent` operations and opens follow-up chat windows that resume the returned session. `--dev` keeps the Go API on the configured `--port` (`9020` by default), starts Vite on a random free port, and proxies `/api` to the API. Pass `--ui-port` to use a specific Vite port. Use `task www:dev` for the local Go-backed Vite proxy with the browser opened, and `task www:build` to rebuild the embedded web UI assets.

### MCP server

```bash
captain mcp
```

Exposes captain commands as MCP tools. Auto-exposes all commands except `sandbox`, `projects`, `container`, `hook`, `ai`, `dod set/clear/run`.

### Utility commands

```bash
# Inspect every process attributed to a copied cmux surface
captain cmux info \
  surface_ref=surface:21 \
  surface_id=65BB4725-B785-48DE-B3FD-31167ECB8300

# Pipe the Copy IDs block directly from the clipboard
pbpaste | captain cmux info

# Inspect a PID and request a Go goroutine stack through gops
captain cmux info 33745 --stack

# Screenshot active browser surface in cmux
captain cmux screenshot

# Kill the process on a TCP port
captain port kill 3000
```

## Build and development

This repo uses `task` as the main task runner.

### Build

```bash
task build
# or
make build
```

Binary output:

```text
.bin/captain
```

### Docs

```bash
task docs:dev
task docs:build
```

The Astro docs site lives in `docs/`. The prompts engine is the first complete section; other Captain areas are scaffolded for future expansion.

### Test

```bash
task test
# or
make test
```

### Lint

```bash
task lint
```

### Install

```bash
task install
```

By default this copies the built binary to:

```text
/usr/local/bin/captain
```

## Docker image

`pkg/container/base/Dockerfile` (embedded into the binary, built as `claude-env:base` by
`captain container`) builds on `flanksource/base-image` and installs a full agent toolchain:

- **Agent CLIs** — one per backend: `claude` (`@anthropic-ai/claude-code`), `codex`
  (`@openai/codex`), `gemini` (`@google/gemini-cli`), plus `tsx` for the `claude-agent`
  SDK bridge
- **Flanksource tools** — `captain`, `gavel`, `repomap` (installed via `deps` from
  `pkg/container/base/deps.yaml`)
- **Go** — toolchain, `task`, `ginkgo`, `golangci-lint`
- **Node** — Node.js 22, `npm`, `pnpm`, `typescript`
- **Browser automation** — `agent-browser` and Playwright Chromium (shared at
  `/ms-playwright`)
- **Shell tooling** — `rg`, `fd`, `bat`, `delta`, `gh`, `jq`, `tree`, `htop`, `lsof`,
  `sqlite3`, `psql`, `tmux`, `shellcheck`, `vim`, `nano`, `zsh`, `fzf`, git

The image is set up to:

- create a user matching host UID/GID
- switch execution using `gosu`
- use `/workspace` as the working directory

Building it requires BuildKit. Set `GITHUB_TOKEN` (or `GH_TOKEN`) before
`captain container build` to avoid GitHub's unauthenticated API rate limit — captain passes
it through as a BuildKit secret, so it never lands in image history. To build by hand:

```bash
DOCKER_BUILDKIT=1 docker build -t claude-env:base \
  --secret id=GITHUB_TOKEN,env=GITHUB_TOKEN \
  pkg/container/base
```

Publishing to `flanksource/captain` on Docker Hub and GHCR (`linux/amd64` +
`linux/arm64`) is the **Publish Image** workflow. It is manual only — dispatch it from
the tag you want to ship, and only once that tag's release exists, since the image
installs `captain` from the latest GitHub release.

## Dependencies and stack

Primary stack:

- **Go 1.25.8**
- **Cobra** for CLI wiring
- **clicky** for formatting/output/flag binding
- **charmbracelet/huh** for the interactive `configure` TUI
- **sandbox-runtime** for sandbox preset handling
- AI SDKs for Anthropic, OpenAI, and Gemini/Google
- shell parsing via `mvdan.cc/sh/v3`

## Quick start

```bash
cd captain
make build
.bin/captain info
.bin/captain history --summary
.bin/captain changes
.bin/captain container list
```

Configure defaults:

```bash
.bin/captain configure
.bin/captain whoami
```

If you want to use hooks:

```bash
.bin/captain hook bash-check install --user
.bin/captain hook dod install --user
.bin/captain hook monitor install
# Opt in to Claude CLI estimate capture:
.bin/captain hook monitor install --capture-cost
```

`--capture-cost` is opt-in because it configures a Claude Code custom status
line, with Claude's normal custom-status-line UI effects. Captain composes with
an existing status-line command and captures `cost.total_cost_usd` as Claude's
client-side session estimate. It is never attributed to model calls or written
as provider-reported billing. Without the flag, monitor installation changes
only the existing lifecycle hooks and Codex notify configuration. Ordinary
Claude transcripts and Codex's local artifacts do not expose a monetary total,
so those histories keep Captain's token-based list-price estimate.

Start the web UI:

```bash
.bin/captain serve
```

## Notes

- Captain is tightly focused on **Claude Code workflows**.
- It is both an analysis tool and an execution/control tool.
- The container/sandbox functionality is a major part of the project, not a side feature.
- Many commands assume the presence of Claude local state under the user’s Claude config/projects directories.
- Runtime defaults are persisted to `~/.captain.yaml`; validated provider tokens are persisted separately to the private `~/.config/captain/vault` file.
