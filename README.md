# Captain

Captain is a Go CLI for working with **Claude Code sessions, hooks, and sandboxes**.

It provides tools to:

- inspect Claude Code project/session history
- summarize tool usage, paths, binaries, and API cost
- install Claude Code hooks
- enforce a session-specific **Definition of Done** gate
- test and use AI providers from the command line
- generate/build/run containerized Claude Code sandboxes
- inspect and clean stored Claude project session data

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

### 4. AI provider utilities

Captain includes provider-agnostic AI utilities under `captain ai`:

- `ai prompt` — send a prompt to a selected backend/model
- `ai models` — list model information
- `ai test` — verify provider connectivity
- `ai fixture` — run a YAML benchmark fixture across multiple Claude configurations and capture a markdown evidence report

Supported backends are inferred from code and dependencies, including:

- Anthropic
- Gemini / Google
- OpenAI-compatible paths via the internal provider layer
- CLI/provider abstractions for local tool-backed backends

### 5. Container sandbox builder

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
├── pkg/ai/                # AI abstraction, provider config, models
├── pkg/ai/fixture/        # YAML fixture runner for Claude configuration benchmarks
├── pkg/bash/              # Bash scanning, classification, rules
├── pkg/claude/            # Claude history, sessions, parsing, formatting
├── pkg/cli/               # Cobra/clicky command implementations
├── pkg/container/         # Sandbox discovery, generation, build/run logic
├── pkg/dod/               # Definition of Done persistence and execution
├── pkg/sandbox/           # Token/preset/sandbox helpers
├── Dockerfile             # Container image for captain/Claude tooling
├── entrypoint.sh          # gosu-based user switching entrypoint
├── Makefile               # Thin wrapper around Taskfile
└── Taskfile.yaml          # Main developer tasks
```

## CLI overview

Top-level commands exposed by `cmd/captain/main.go`:

```bash
captain history
captain info
captain cost
captain sandbox
captain ai
captain dod
captain hook
captain projects
captain container
```

### History

```bash
captain history
captain history --summary
captain history --all
captain history --tool Bash --since now-7d
captain history --category git --compact
```

Useful flags include:

- `--file`
- `--tool`
- `--dir`
- `--category`
- `--approved`
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

The included `Dockerfile` builds on `flanksource/base-image` and installs:

- Node.js
- git, gh, jq, vim, nano, zsh, fzf, etc.
- Claude Code via `@anthropic-ai/claude-code`

The image is set up to:

- create a user matching host UID/GID
- switch execution using `gosu`
- use `/workspace` as the working directory

## Dependencies and stack

Primary stack:

- **Go 1.25.8**
- **Cobra** for CLI wiring
- **clicky** for formatting/output/flag binding
- **sandbox-runtime** for sandbox preset handling
- AI SDKs for Anthropic, OpenAI, and Gemini/Google
- shell parsing via `mvdan.cc/sh/v3`

## Current state

This README reflects the current code layout.

At the time of generation, `go test ./...` in this checkout does **not** fully pass. Observed issues included:

- a build failure around `pkg/claude/tools`
- failing container tests
- AI integration tests requiring valid external provider credits/config

So treat this repository as **active/in-progress** rather than guaranteed green in the current local state.

## Quick start

```bash
cd captain
make build
.bin/captain info
.bin/captain history --summary
.bin/captain container list
```

If you want to use hooks:

```bash
.bin/captain hook bash-check install --user
.bin/captain hook dod install --user
```

## Notes

- Captain is tightly focused on **Claude Code workflows**.
- It is both an analysis tool and an execution/control tool.
- The container/sandbox functionality is a major part of the project, not a side feature.
- Many commands assume the presence of Claude local state under the user’s Claude config/projects directories.
