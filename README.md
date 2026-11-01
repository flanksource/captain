# Captain

Captain is a Go CLI and embeddable runtime for AI prompts, agent execution, verification and session history. It runs API, agent, CLI and cmux runtimes selected by model, records Claude and Codex sessions, brokers approvals, and exposes a local web workbench.

## Quick start

Build with the Go version declared in [go.mod](go.mod), Node.js/pnpm for the web assets, and [Task](https://taskfile.dev) installed:

```sh
task www:build
make build
.bin/captain configure
.bin/captain prompt list
.bin/captain prompt run --model agent:sonnet -p 'Explain this repository'
.bin/captain serve --open
```

Local agent runtimes require their CLI and its login. API runtimes require provider credentials; `captain configure openai` securely validates and stores an API token. `captain whoami --models=false` inspects runtime readiness. Server/session persistence uses PostgreSQL; startup logs identify the selected database.

## Prompts

Keep instructions and runtime declarations in a `.prompt` file:

```yaml
---
name: Review a package
model: agent:sonnet
permissions:
  access: read-only
budget:
  timeout: 10m
input:
  default:
    target: pkg/cli
---
{{role "user"}}
Review {{target}} for correctness. Report findings with file paths and lines.
```

Save as `review.prompt`, preview the effective request, then execute:

```sh
captain lint promptfile review.prompt
captain prompt render review.prompt -V target=pkg/promptrun --json
captain prompt run review.prompt -V target=pkg/promptrun
captain prompt run review.prompt -M agent:sonnet -M agent:gpt-6.1-sol
```

Render performs no model call or setup. Files are templates; inline `-p` and piped text are literal unless variables or `{{input}}` opt into templating. Piped input binds to `input`, a named `-V key=-`, or is appended when unused. Use `--attach` for files/URLs, `--preset` for reusable runtime layers and `--no-stream --json` for a buffered structured result.

```sh
captain prompt --help
captain prompt run --help
captain prompt render --no-color --help
captain prompt --schema
```

`prompt` and every child command have colored terminal help, plain output with `--no-color`, `NO_COLOR` or redirection, and Markdown in detected agent sessions. Root help covers authoring; child help adds command examples and generated flags. See the [Prompt CLI guide](docs/src/pages/prompts/cli.mdx), [format](docs/src/pages/prompts/format.mdx), [runtime composition](docs/src/pages/prompts/runtime.mdx) and [sources/API](docs/src/pages/prompts/sources-api.mdx).

## Command map

| Area | Commands | Documentation |
| --- | --- | --- |
| Prompts | `prompt list/get/create/update/delete/render/run/observe`, `lint promptfile` | [Prompt CLI](docs/src/pages/prompts/cli.mdx) |
| Runtime configuration | `configure`, `whoami`, `runtime-preset`, `permissions explain/matrix/convert` | [Configuration](docs/src/pages/configuration/index.mdx) |
| Verification | `verify`, prompt `workflow.verify` | [Verification](docs/src/pages/agents/verification.mdx) |
| Sessions | `sessions`, `history`, `info`, `cost`, `changes`, `browser ls` | [Sessions](docs/src/pages/sessions/index.mdx) |
| Web/API | `serve`, `mcp`, `token` | [Serve](docs/src/pages/serve/index.mdx) |
| Benchmarks | `ai fixture`, `prompt observe` | [Fixtures](docs/src/pages/fixtures/index.mdx) |
| Isolation | `container`, `sandbox`, `sandbox credentials`, `sandbox git-agent` | [Sandboxes](docs/src/pages/sandboxes/index.mdx) |
| Gavel integration | `gavel todos` embeds Captain | [TODO lifecycle](docs/src/pages/todos/index.mdx) |

`captain ai prompt` aliases `captain prompt run`. Other AI utilities include `ai agent` for explicit generate/verify workflows, `ai models`, `ai test`, `ai tokens` and `ai mock`. Use each command's help for its current flags. A runtime is `(model, mode)`; mode is `api`, `agent`, `cli` or `cmux`, with provider derived from the model. Configure provider defaults with `captain configure openai --mode agent --model gpt-6.1-sol --effort high --active`.

## Verification and embedding

```sh
captain verify --command 'make build' --command 'go test ./pkg/ai/prompt'
captain verify --fixture acceptance.md --cwd /path/to/repo
```

Fixtures require a runner, configured in `~/.captain.yaml`:

```yaml
verify:
  fixtureRunner: [gavel, fixtures, run, --format, captain-verify-report]
```

The external runner receives fixture Markdown on stdin plus cwd/changed-file arguments and returns NDJSON progress followed by one report. Captain's `verify` command is local only. Workflow prompts can declare commands, LLM judges and fixtures under `workflow.verify`; failed verdicts feed the next iteration up to `maxIterations`.

Go hosts call `promptrun.Preflight` and `promptrun.Run` with a resolved spec, prepared attachments, timeout and optional recording/approval settings. Gavel uses this same seam and owns its commit finalization. See [Embedding a run](docs/src/pages/agents/embedding.mdx), [Approvals](docs/src/pages/agents/approvals.mdx), [runtime composition](pkg/runtimeprofiles/README.md), [prompt rendering](pkg/ai/prompt/README.md), [recording](pkg/promptrun/README.md), [chat](pkg/aichat/README.md) and [budget rules](pkg/budgets/README.md).

## History, hooks and utilities

```sh
captain history --summary
captain history --tool Bash --since now-7d
captain cost --group-by model
captain changes --session-id SESSION_ID
captain sessions get SESSION_ID --json --limit 0
captain browser ls --all
```

History filters include tool, category, path, time range and session. Changes defaults to the latest session in the current directory; `--agents=false` excludes nested agents. Token costs distinguish recorded usage, provider-reported cost and estimates; see [Sessions](docs/src/pages/sessions/index.mdx).

```sh
captain hook bash-check install --user
captain hook monitor install
captain hook monitor install --capture-cost
captain browser install --global --dry-run
```

The bash-check hook scans shell calls. Monitor hooks track session lifecycle; `--capture-cost` opts into Claude's custom status line and records its client-side estimate separately from provider billing. Installation refuses a higher-precedence project status line that would override it. Browser installation previews/merges an agent-browser launch plugin; see [browser attribution](docs/src/pages/sessions/index.mdx#browser-sessions).

`captain cmux info` accepts copied surface IDs, a PID or stdin and reports process trees, resource use and listening ports; `--stack` requests Go stacks through gops. `captain cmux screenshot` captures the active browser surface. `captain port kill PORT` stops the port's listener. `projects list` and `projects clean` inspect and maintain stored Claude project history.

## Development

```sh
make build
make lint
gavel test ./pkg/ai/prompt
make test
task install
```

`make` forwards to [Taskfile.yaml](Taskfile.yaml). Build writes `.bin/captain`; install uses `go install` and its configured Go bin directory. Rebuild `task www:build` before the Go binary when changing embedded UI assets. `task www:dev` starts the Go-backed Vite development proxy.

```sh
task docs:dev
task docs:build
pnpm --dir docs check
```

The Astro site covers prompts, runtime composition, approvals, verification, sessions, server configuration, fixtures, sandboxes and the Gavel integration. Start at [the documentation overview](docs/src/pages/index.mdx). Source docs and examples should match the current generated CLI/schema; historical designs in `docs/plans` remain design records.

## Source layout

| Path | Owns |
| --- | --- |
| `cmd/captain` | CLI wiring |
| `pkg/api` | Typed runtime, approval and verification contracts |
| `pkg/ai` | Providers, model catalog, prompt rendering, agent loop and middleware |
| `pkg/promptrun` | Execution admission, run lifecycle and recording |
| `pkg/runtimeprofiles` | Preset catalogs and layered composition |
| `pkg/session`, `pkg/claude` | Unified sessions and transcript parsing |
| `pkg/database`, `migrations` | PostgreSQL storage and migrations |
| `pkg/cli`, `pkg/cli/webapp` | Command handlers and embedded React UI |
| `pkg/captainconfig` | Saved defaults and credential publishing configuration |
| `pkg/sandbox`, `pkg/container`, `pkg/gitagent` | Isolation and remote execution |
| `pkg/bash`, `pkg/cmux` | Shell classification and terminal integration |
| `docs` | Astro documentation |
