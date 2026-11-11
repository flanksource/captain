# Inline tool environment: placement and credentials

Status: Phases 0–3 implemented (2026-10-23); Phase 4 pending open questions 1–2 · 2026-10-23 · follows the inline-tools change (`pkg/api/inline_tool.go`, `pkg/ai/tools/exectool`, `pkg/promptrun/inline_tools.go`)

## Context

An inline tool is a script the model calls. Two questions decide what the script can reach, and today neither one is configurable:

1. **Placement.** Does the script run inside the run's sandbox, or outside it on the host next to captain?
2. **Credentials.** How do the script's connections reach it?
   - `none`: the script gets no credentials.
   - `direct`: the script holds the real credentials and performs the credentialed operation itself. On the host, this makes the tool the proxy: the sandboxed agent only ever sees the tool's result.
   - `proxied`: the script gets credential-free configuration pointing at a host-side proxy that injects the credentials on the way out. This is the pattern `pkg/ai/fixture/kubeproxy` already uses for fixtures.

Today every inline tool runs **on the host with direct credentials**, whatever the sandbox:

- `promptrun.Input.Verify.Wrap` is never set by `captain prompt run` or `captain ai agent` (`pkg/cli/prompt_run_live.go:60`, `pkg/cli/ai_agent.go:224`), so `exectool` calls `shell.RunCmd` with a nil `Wrap`.
- The docs say the opposite: `docs/src/pages/prompts/format.mdx` claims "In a sandboxed run, the script is confined by the same wrapper as `workflow.verify` commands." That sentence is false.

The goal is to make placement and credentials explicit, declared per tool and checked against what the run's sandbox can support. A combination the sandbox cannot provide is refused before the model is called. Nothing is silently downgraded.

```yaml
tools:
  deploy_status:
    exec: kubectl get deploy -o json
    readonly: true
    placement: host          # host | sandbox
    credentials: proxied     # none | direct | proxied
    connections:
      kubernetes: {kubeconfig: {valueFrom: {secretKeyRef: {name: prod, key: kubeconfig}}}}
  release_notes:
    exec: ./hack/notes.sh
    placement: sandbox
    credentials: none
```

## Gaps

| # | Gap | Evidence |
|---|-----|----------|
| G1 | Placement and credentials are not declarable; the docs claim tool scripts are confined, but they never are | `Verify.Wrap` is unset at both `promptrun.Run` call sites; `format.mdx` inline-tools section |
| G2 | No run-scoped wrapper is available to tools. The only `CommandWrapper` is built per agent CLI process | `provider/cli.go:152-215` (`newSandboxedCommand`); `gitagent/hookmain.go:105` (`ResolveHookWrap`, git-agent hooks only) |
| G3 | commons-db `Exec.Wrap` receives the full prepared environment (host allowlist included). The docker wrapper would forward host `PATH`/`HOME` into the container | `commons-db/shell/shell.go` `wrapCommand`; captain deliberately passes only declared env to agent wrappers (`provider/cli.go:218` `declaredSetupEnv`) |
| G4 | Connections are materialized on **every** tool call: a new `.creds/` directory in the workspace cwd, plus `gcloud auth` / `az login`, which change host state | `commons-db/connection/environment.go` (`saveConfig` writes into `cmd.Dir`, L253-308); `exectool.execFor`, then `RunCmd` per call |
| G5 | No proxied credential mode is wired anywhere. Two working building blocks exist: `kubeproxy` (a credential-free kubeconfig plus an auth-injecting reverse proxy) and `gitagent/proxy` (HTTP grants with placeholder substitution). Neither is used on the run path | `pkg/ai/fixture/kubeproxy/proxy.go:78,215`; `pkg/gitagent/proxy/grants.go:36,143`; `gitagent_deploy.go:116` ("egress credential proxy has no callers") |
| G6 | Under docker, caller tools are unreachable for **any** placement. The loopback MCP server binds `127.0.0.1` on the host; the container has no `--add-host` and the MCP config temp file is not mounted. Nothing refuses this combination | `callertools/runtime.go:123`; `sandbox/adapter/container.go:212-242`; `promptrun/preflight.go` checks only the mode |
| G7 | Native sandboxes are provider-enforced (claude `--settings`, codex), so there is no wrapper that could hold a tool. git-agent runs refuse inline tools outright | `adapter/none.go`; `promptrun/inline_tools.go` (supplied Provider) |
| G8 | Tools inherit the run's `setup.connections`, so a tool that should run with `none` silently gets them. Separately, run-level connections give the agent the same secrets the tool was meant to keep away from it | `exectool.Workspace.execFor`; `provider/cli.go:180-184` (agent env = `req.Setup.Env`) |

## Design

### Fields

They are added to `api.InlineTool`; a spec-level `toolDefaults` is deferred.

- `placement: host | sandbox`. The default is `host`, which is today's behavior.
- `credentials: none | direct | proxied`. The default is derived and never guessed:
  - The tool's connections (its own plus any it inherits) are empty → `none`.
  - Otherwise, `placement: host` → `direct`. This is today's behavior.
  - Otherwise, `placement: sandbox` → **refused** until `credentials` is set. Secrets never enter a sandbox implicitly.
- What counts as credentials: `connections`, plus `envVars` that use `valueFrom`. Static `envVars` and `dotenv` are configuration and pass through in every mode.

### Semantics

| credentials | host placement | sandbox placement |
|---|---|---|
| `none` | Run connections are not inherited. A tool that declares `connections` or secret `envVars` is a validation error. | Same rule, inside the sandbox. |
| `direct` | Real credential files and env on the host. **The tool is the proxy**: the agent sees only the result. | Real credentials enter the sandbox. Allowed only with this explicit value; lint warns. |
| `proxied` | Credential-free config pointing at a host proxy on `127.0.0.1`. Useful for auditing and per-call scoping. | Credential-free config pointing at a host proxy the sandbox can reach. The recommended sandboxed mode. |

### Proxied: capability per connection type

This is declared as data, not as switches spread through the code.

| Connection | Proxied via | Status |
|---|---|---|
| `kubernetes` | `kubeproxy.Start`, then `WriteKubeconfig` (no user credentials) → `KUBECONFIG` | Supported |
| HTTP(S) `grants` (new tool field, the `Grant` shape) | The egress proxy (moved from `gitagent/proxy` to `pkg/credproxy`); `SandboxEnv` placeholders plus `HTTP_PROXY`/`HTTPS_PROXY`. HTTPS works through CONNECT, which the proxy terminates with a per-run CA that the tool trusts | Supported |
| `aws`, `gcp`, `azure` | No proxy exists | Refused with "use credentials: direct on placement: host" |

### Placement: capability per sandbox kind

This is data on the sandbox descriptor in `pkg/api/registry/sandboxes.go`: a new `ToolPlacements` field next to `Capabilities`.

| Run sandbox | `host` | `sandbox` |
|---|---|---|
| off | ✓ | ✗ (there is no sandbox to place the tool in) |
| native | ✓ (the tool runs outside the provider's sandbox) | ✗ (provider-enforced; no wrapper) |
| docker | ✓ once G6 is fixed | ✓ once G6 is fixed (container `Wrap`) |
| git-agent | ✗ (inline tools are refused) | ✗ |

Preflight refuses unsupported cells, naming the tool, the sandbox and the supported alternative. `captain sandbox matrix` (or the existing `captain permissions matrix`) prints the table.

### Lifecycle

The tool environment is prepared **once per run**, not per call:

- `exectool.Workspace` becomes a run plugin.
- **PreRun**, ordered after setup: resolve connections, start proxies, write credential files into a private directory under `BaseDir` (not the workspace cwd), and resolve the wrapper.
- **Post at PhaseRun**: stop the proxies and remove the credential files.
- Each call then runs `shell.RunCmd` with the prepared env and the shared wrapper.

## Phases

### Phase 0: Stop the false claim (docs only)

- [ ] `format.mdx`: state that inline tools run on the host with their declared credentials, whatever the sandbox, until placement lands.
- **Backwards compatibility:** no behavior change.

### Phase 1: Declared fields and refusal matrix (G1, G7, G8)

- [ ] `api.InlineTool`: `Placement`, `Credentials` enums with the derived-default rule; validation for `none` combined with declared credentials.
- [ ] `registry` sandbox descriptors: `ToolPlacements` data; `api.ToolEnvironmentSupport(sandboxKind, placement, credentials, connectionKinds)`, returning a reason when unsupported.
- [ ] `promptrun` preflight: check every inline tool against the resolved sandbox (`cfg.ResolvedSandbox()`) for every fallback candidate.
- [ ] `exectool`: `credentials: none` drops inherited connections and secret `envVars`.
- [ ] `captain lint promptfile`: warn on `direct` combined with `sandbox`. Inform when a tool uses `none`/`proxied` but the run's own `setup.connections` hand the same secrets to the agent anyway (G8).
- [ ] Tests: a matrix `DescribeTable` in `pkg/api`; refusal tests in `promptrun/preflight_callertools_ginkgo_test.go`.
- **Backwards compatibility:** the defaults reproduce today's behavior (`host`, plus `direct` when connections exist). The only newly refused case is `placement: sandbox`, which did not exist before.

### Phase 2: Run-scoped tool environment (G4)

- [ ] commons-db: add `Exec.CredentialsDir` (or a `SetupEnv` option) so connection files land outside `cmd.Dir`. Test that the workspace cwd stays clean.
- [ ] `exectool.Workspace`: becomes a PreRun + Post(PhaseRun) plugin. It prepares the env once and reuses it per call; `promptrun.Hooks` keeps it after setup.
- [ ] Test: the call count does not change the number of `.creds` directories, and cleanup runs at PhaseRun even when the run fails.
- **Backwards compatibility:** same env per call. Credential files move out of the workspace (a behavior improvement).

### Phase 3: Proxied credentials, host placement (G5)

- [ ] Move `kubeproxy` out of `pkg/ai/fixture` into a shared `pkg/credproxy/kube`. Fixtures and observe-capture import it from there; no copy is kept.
- [ ] Move `pkg/gitagent/proxy` to `pkg/credproxy` (no importers outside its own tests) and update the spec references.
- [ ] HTTPS: the proxy serves CONNECT for a granted `https` destination by terminating TLS itself:
  - It mints a leaf certificate for the CONNECT host from a per-proxy CA.
  - It then runs every request inside the tunnel through the same grant pipeline as plain HTTP: scope, misplaced-placeholder rejection, substitution, and upstream TLS validated against the granted host.
  - CONNECT to an ungranted destination is refused (deny by default, as now).
  - The CA's private key never leaves the proxy process. Only the CA certificate is written out, for the tool to trust.
- [ ] Add a `grants` field on `InlineTool` with the `Grant` shape. Values resolve through commons-db `types.EnvVar` (the same resolver as `envVars`).
- [ ] Workspace PreRun starts the needed proxies on `127.0.0.1` and exposes these to the tool:
  - `KUBECONFIG`, `HTTP_PROXY`/`HTTPS_PROXY` (with `NO_PROXY=127.0.0.1,localhost` so the kube proxy stays direct), and the placeholder env.
  - The CA bundle through `SSL_CERT_FILE`, `CURL_CA_BUNDLE`, `REQUESTS_CA_BUNDLE`, `NODE_EXTRA_CA_CERTS` and `GIT_SSL_CAINFO`.
  - Real values never appear in the tool's env.
- [ ] Tests:
  - A tool runs `kubectl`-free curl against a fake API server through the proxy, and its env contains no secret.
  - A grant violation (wrong path or method) is refused and audited.
- **Backwards compatibility:** opt-in.

### Phase 4: Sandbox placement on docker (G2, G3, G6)

- [ ] commons-db: `wrapCommand` passes the wrapper only the declared env (dotenv, envVars, connection outputs), never the host allowlist. This mirrors captain's `declaredSetupEnv`. A nil env from the wrapper still runs with the full prepared env.
- [ ] `pkg/sandbox/adapter/container.go`:
  - Add `--add-host host.docker.internal:host-gateway`.
  - Expose a `HostAddress()` the run can advertise.
  - Refuse a script cwd that is outside the mounted paths.
- [ ] `callertools`: bind an address the container can reach. Rewrite the advertised MCP URL to `host.docker.internal`. Pass the MCP config inline instead of as a host temp file. This makes host-placed tools work under docker too.
- [ ] Proxies listen on the same reachable address when the tool is placed in the sandbox.
- [ ] `promptrun`: resolve the run's sandbox once, then pass its `CommandWrapper` to `exectool` for `placement: sandbox` tools only. Host tools stay unwrapped.
- [ ] Tests:
  - A `docker` gated integration test (skip when docker is unavailable): the tool runs in the container, sees the proxied `KUBECONFIG`, and cannot read a host credential path.
  - An agent CLI in the container reaches the MCP endpoint.
- [ ] Docs: `format.mdx` and the sandbox docs get the matrix, and `sandboxes/index.mdx:76` drops its inaccurate `tokens` claim.
- **Backwards compatibility:** `Exec.Wrap` is unreleased, so its semantics can change freely. Caller tools under docker go from silently broken to working.

## Open questions

1. **Linux reachability.** On Docker Desktop, `host.docker.internal` reaches the host's loopback; on Linux, `host-gateway` resolves to the bridge IP, and services bound to `127.0.0.1` are unreachable. Should Phase 4 bind to the bridge gateway IP on Linux, or require Docker Desktop?
2. **Native confinement.** Should `placement: sandbox` under a native or off run be offered through the internal srt hook profile (no network, workspace-only writes)? That would give host runs a confined option without docker. It needs `srt.go:219` to accept arbitrary commands under the hook profile.
3. ~~**HTTPS grants.**~~ Resolved 2026-10-23: the proxy terminates CONNECT with a per-run CA (Phase 3).

## Out of scope

- git-agent placement: tools would need an MCP route back across the sidecar boundary.
- AWS/GCP/Azure proxying: this would need an STS/metadata-endpoint emulator.
- A spec-level `toolDefaults` block.
- The `captain serve` exposure: refusing `exec` tools over HTTP without a wrapper is already a separate follow-up.

## Verification

- Every phase runs focused `gavel test` / `go test` on `pkg/api`, `pkg/ai/tools/exectool`, `pkg/promptrun`, commons-db `shell`, and the new proxy package, followed by `make build` and lint on the touched packages.
- Smoke tests:
  - `captain prompt run hack/inline-tool.prompt` with each mode on `--sandbox off`. Then the Phase 4 docker run on `--sandbox docker --model cli:sonnet`.
  - Confirm the tool result, the absence of secrets in the tool's env (an `env` probe tool), and the refusal messages for unsupported cells.
