# Plan: inline tools for git-agent runs (`placement: supervisor | sidecar`, credential modes, least privilege)

## Context

A git-agent run moves the whole job onto an enrolled remote agent. Today such a run refuses inline tools, for four reasons:

- The dispatch carries only `{prompt, system, model, mode, provider, effort, timeout}` (`pkg/gitagent/dispatch.go:23`).
- The sidecar's runner builds a bare request from that payload (`pkg/cli/gitagent_runtask.go:82`).
- On the supervisor, no caller-tool server runs, because no provider runs locally (`pkg/cli/ai_sandbox_remote.go:97`).
- `GitAgentSandbox.ToolPlacements` is empty (`pkg/api/registry/sandboxes.go:41`).

**Goal:** the agent on the sidecar sees the prompt's tools as one MCP server. Each tool is placed in one of two ways:

- **`placement: supervisor`**: it runs on the supervisor host.
- **`placement: sidecar`**: it runs on the sidecar host, next to the worktree.

Each tool also declares where its credentials come from. The modes are listed in the matrix below, and every mode gives the tool the least it needs.

**Facts this design relies on:**
- **The sidecar can already reach the supervisor.** It relays results by pushing to the supervisor's mailbox (`relay.go:177`), served over SSH by `git-agent serve --role mailbox` (`server.go`) or over HTTPS by `captain serve --tls` (`serve_git.go:77`). That route is authenticated by the sidecar's enrolled key or token, and the plan reuses it.
- **The SSH server only accepts exec sessions.** Its commands are `captain-enroll` and `git-receive-pack`, with no port forwarding (`server.go:96-111`).
- **`callertools` already serves MCP the way the agent needs.** It uses HTTP on loopback with a per-run bearer token, and applies approvals on every call (`pkg/ai/callertools/runtime.go`). On the sidecar, the agent CLI keeps talking to a local `callertools` (`StartCLI`, `cli.go:33`).
- **The agent and `run-task` share an OS user, and the agent inherits the sidecar's full environment.** `LaunchAgent` passes `ScrubGitEnv(os.Environ())` to the agent (`pkg/gitagent/workspace.go:110`). This drives most of the security review below.

## Vocabulary

**Placement:** each sandbox descriptor lists the placements it allows, in order, and the first entry is its default.
- Off and native: `[host]`.
- Git-agent: `[supervisor, sidecar]`.

Any placement a descriptor doesn't list is refused, with an error naming the allowed values.

**Credentials:**

| mode | secret values come from | where the real values exist at run time | what the tool process sees |
|---|---|---|---|
| `none` | nowhere | nowhere | only static configuration |
| `direct` | the supervisor (or the host) | the supervisor tool's environment | real values |
| `proxied` | the supervisor (or the host) | a proxy on the supervisor | placeholders and the proxy's address |
| `sidecar` | the sidecar's own environment, files and cloud identity (IRSA, EKS pod identity, service account) | the sidecar tool's environment | real values |
| `sidecar-proxied` | fetched from the supervisor once and cached in `run-task` memory | a proxy inside the sidecar's `run-task` | placeholders and the proxy's address |

**Allowed combinations:**

| placement | none | direct | proxied | sidecar | sidecar-proxied |
|---|---|---|---|---|---|
| `host` / `sandbox` (local) | ok | ok | ok | refused | refused |
| `supervisor` (default) | ok | ok | ok | refused | refused |
| `sidecar` | ok | **refused**: secrets would cross to the sidecar | ok: the proxy stays on the supervisor and is reached through the tunnel | ok | ok |

**Validation for the new modes** (`validateEnvironment`):
- **`sidecar`:** the tool's own `envVars`, `dotenv` and `connections` are allowed, unproxiable ones included, because the sidecar resolves them itself. Grants are refused. The supervisor never resolves these references.
- **`sidecar-proxied`:** grants are required. Secret `envVars` and any `connections` are refused, so v1 is HTTP(S) grants only; Kubernetes uses `proxied`.

**Defaults:** a `sidecar` tool with secrets must state `credentials:` explicitly. A `supervisor` tool resolves as `host` does today.

## Design

```
sidecar host (agent's OS user)                         supervisor host
agent CLI ──MCP──▶ callertools (run-task)
                    ├─ sidecar tool (none|sidecar) ─▶ exectool, env resolved on the sidecar
                    ├─ sidecar tool (sidecar-proxied) ─▶ exectool ─▶ credproxy in run-task ─▶ upstream
                    │                                  values from ══ "creds/<t>" (once) ══╗
                    ├─ sidecar tool (proxied) ─▶ exectool ─▶ forwarder ══ "egress|kube/<t>" ══╣ tunnel(task, service)
                    └─ supervisor tool ─▶ MCP forwarder ════════════ "tools" ════════════════╣ SSH exec / HTTPS upgrade
                                                    mailbox serve: checks agent, task, ◀═════╝
                                                    service, then splices to loopback ─▶ callertools / credproxy /
                                                                                         kubeproxy / creds service
```

## Security review (least privilege)

The findings below are folded into the phases, and the phase that handles each one is in brackets.

| # | Finding | Mitigation |
|---|---|---|
| S1 | **The agent inherits the sidecar's whole environment** (`workspace.go:110`). Every sidecar credential is already the agent's, so a `sidecar` tool would gain no isolation from the agent. | `LaunchAgent` passes an allowlist: system variables, provider auth for the selected runtime, and the `CAPTAIN_*` task variables. Everything else is resolved by `run-task` only for the tool that declares it. [P1] |
| S2 | **Same OS user, no isolation.** `run-task`, tool processes and the agent share a user. On Linux a same-user process can read `/proc/<pid>/environ`, and everything on disk is readable unless the agent is confined: `task.json`, the sidecar key, the per-run CA, the tools' private directories. | The agent runs under the SRT/native policy with read access denied to the task state dir, the sidecar key and config dir, and every tool's private dir (`hostCredentialDenyRead`, `srt.go:316`, extended). Docs state the remaining guarantee plainly: `sidecar` and `sidecar-proxied` keep secrets out of the agent's environment and context, not out of a same-user process that escapes its confinement. Full separation needs a separate user or a container, which is a follow-up TODO. Lint warns on `sidecar`/`sidecar-proxied` when the agent isn't confined. [P1, P5] |
| S3 | **Proxies on loopback have no authentication.** Any local process can use a grant while the run lasts. This also applies to today's local `proxied` tools. | Each proxy gets a per-tool secret. credproxy requires `Proxy-Authorization`, carried in the proxy URL's userinfo (`http://tool:<secret>@127.0.0.1:port`). kubeproxy requires a bearer token from its own kubeconfig, and strips it before forwarding. [P2] |
| S4 | **The tunnel could become an SSRF primitive.** | `OpenService` splices only to loopback addresses taken from the 0600 `services.json` the supervisor run wrote, never to an address the client supplies. Opening requires: the authenticated agent equals the task's dispatched agent, the task has no verdict, and the service is registered. Every open is logged (task, service, agent). [P3] |
| S5 | **`sidecar-proxied` sends real secrets to the sidecar.** | The `creds/<tool>` service returns only the values of that tool's own grants, and only once (it deregisters after the read). Values are cached in `run-task` memory as `text.SensitiveString`, never written to disk or logs, and cleared on `Close`. The response is checked against the tool's declared grants. The supervisor logs which grant names were released. [P5] |
| S6 | **The MCP bearer token sits in `task.json`, which the agent can read if it isn't confined.** | Accepted: the token reaches only the tools this run already exposes, and the supervisor's `callertools` still applies approvals and the run's tool policy. The token is per run and revoked on close. [P4] |
| S7 | **The agent can use a grant without going through the tool's script.** It can reach the proxy or tunnel directly, under S2 and S3. | A grant's methods, paths and hosts are the real capability boundary, and the docs say so. Lint warns on a grant with no `methods` or with path `/` on a `readonly` tool. [P6] |
| S8 | **Each service lives longer than it needs to.** | Services are registered right before dispatch and removed when `AwaitOutcome` returns or the run is cancelled. `creds/*` is single-use. A grant's lifetime is the run's. [P3] |
| S9 | **Unattended approvals.** `run-task` has no approval broker. | A sidecar tool whose permission resolves to `ask` is refused on the supervisor before dispatch. Supervisor tools are approved by the supervisor's broker. The forwarding definitions on the sidecar are auto-approved only because the supervisor decides. [P4] |
| S10 | **The sidecar and supervisor could run different versions.** | `TaskPayload.Requires` (for example `"tools"`, `"sidecar-proxied"`), and `run-task` refuses any requirement it doesn't know. A sidecar whose serve process rejects `captain-tunnel` fails the run loudly. [P4] |

## Phases

### P1: vocabulary and the agent's environment
- [x] `pkg/api/registry/sandbox.go`:
  - add `ToolPlacementSupervisor` and `ToolPlacementSidecar`;
  - `Sandbox.ResolveToolPlacement` replaces `ToolPlacement.Effective`;
  - `ValidateToolPlacement` names the allowed values.

  `sandboxes.go`: GitAgent allows `[supervisor, sidecar]`.
- [x] `pkg/api/inline_tool_environment.go`:
  - add `ToolCredentialsSidecar` and `ToolCredentialsSidecarProxied`;
  - apply the allowed-combinations matrix and the validation rules above;
  - update `ResolveCredentials`.

  Callers of `Effective()` move over: `promptrun/inline_tools.go`, `exectool/workspace.go`, `pkg/ai/prompt/validate.go`.
- [x] S1, implemented in `run-task`, not `LaunchAgent`: `LaunchAgent` starts `run-task`, which needs the sidecar's environment to resolve `credentials: sidecar` tools. Deviation: there is no process split. Once the tools' environments are prepared, `scrubAgentEnv` (`gitagent_runtask_tools.go`) reduces `run-task`'s own environment to `keepAgentEnv`, and every backend's agent inherits that. `keepAgentEnv` covers system variables, `CAPTAIN_*`, and every provider's `EnvVars`. The scrub runs only when a sidecar tool holds credentials, so tasks without such tools are unchanged.
- [x] S2, implemented in P5. The worktree lives inside the task state dir (`<repo>/captain/tasks/<task>/worktree`), so `agentDenyRead` denies specific paths: `task.json`, the tools dir, the captain config, and the host, agent and dispatch keys, token, TLS key and relay key/token. When `denyRead` is set, the run is forced to native with `DeniedReadRoots`. This also fixed a gap: a `sandbox: off` provider option used to override `req.Sandbox`, so run-task agents ran unsandboxed. Only native Claude enforces this. Codex rejects `deniedReadRoots` (`sandbox_codex.go:155`), so such a task fails when it launches a Codex agent.
- [x] Tests: matrix tables in the placement and credential suites; `keepAgentEnv` and the confinement/Claude translation in `gitagent_runtask_test.go`. Not done: an e2e dump of the agent's environment.
- **Backwards compatibility:** local placements are unchanged. Deviation: the `agentEnv` passthrough option was dropped, because the scrub applies only to tasks with credentialed sidecar tools, which are new.

### P2: authenticated proxies (S3)
- [x] `credproxy.Proxy.Secret`, checked on every request and CONNECT. A request without the matching `Proxy-Authorization` gets 407, and the header never reaches upstream. `credproxy.ProxyURL(addr, secret)` and `credproxy.NewSecret()`.
- [x] `kubeproxy.Start(Options{Kubeconfig, Token})`. The package-level `WriteKubeconfig(dir, Endpoint{URL, Token})` writes the token, which the proxy requires and strips. The fixture and observe-capture kube proxies get tokens too.
- [x] `exectool/proxied.go` splits into `serveProxies` / `ServeEgressProxy` (where the secrets are) and `ProxiedEnv(dir, ProxyEndpoints, grants)` (the tool's environment). Local tools call both halves.
- [x] Tests: 407 without or with the wrong secret (plain and CONNECT), the secret never reaching upstream, a 401 kube proxy without its token, the kubeconfig carrying only the proxy token, and a tool's `curl` without the userinfo getting 407. The existing suites pass.
- **Backwards compatibility:** internal only. Tool scripts get the same environment variable names, and only the URL gains userinfo.

### P3: the tunnel (S4, S8)
Done. Liveness differs from the first draft: a tunnel is refused once the services are withdrawn, not once a verdict exists, because a rejected attempt is retried and still needs its tools. `Dispatch` registers the services with the task state (`DispatchRequest.Services`) and withdraws them itself if the push fails; the client half is `tunnel_client.go`, and SSH dialing is shared with the git transport (`dialSSH`).

- [x] `pkg/gitagent/tunnel.go`:
  - `WriteServices`, `ReadServices` and `RemoveServices`, kept beside the task state;
  - `OpenService(repo, identity, task, service)`, which applies the S4 checks, dials only loopback, and logs every open;
  - `DialService(ctx, TransportTarget, task, service)`, which reuses the relay transport;
  - `ServeForwarder`.
- [x] `server.go`: a `captain-tunnel <task> <service>` exec command. `httpserver.go`: an `Upgrade: captain-tunnel` route. Both go through `OpenService`.
- [x] Tests (`tunnel_ginkgo_test.go`, 14 specs):
  - an echo service round-trips over SSH and HTTPS;
  - refused for: unknown task, another agent, unregistered service, non-loopback entry;
  - refused once `RemoveServices` withdraws the services.
- **Backwards compatibility:** additive. An older serve process fails the run loudly (S10).

### P4: `placement: supervisor` (S6, S9, S10)
- [x] `api.RemoteExecutor.Execute(ctx, spec, RemoteExecOptions{CallerTools, CredentialReleases, ToolProxies})`. `gitAgentSandbox.Execute` registers the services through `Dispatch` and removes them after `AwaitOutcome`.
- [x] `remoteExecProvider.Execute` (`ai_sandbox_remote.go`) starts `callertools.StartCLI` with the run's final config. `promptrun.Input.Relocated` builds the provider from the final config.
- [x] `TaskPayload.Tools{Name, Path, Headers}` and `Requires` with `CheckRequirements`.
- [x] `run-task`: without sidecar tools, the forwarded endpoint goes straight into `cfg.CallerTools`. With sidecar tools, `callertools.Forward` lists the supervisor's tools and each forwarding definition sits beside the sidecar's in `cfg.Tools`.
- [x] Tests: payload requirements in `gitagent_runtask_test.go`; e2e `TestDispatchServesSupervisorToolsToTheAgent`. Not done: an e2e `ask` tool decided by the supervisor broker.
- **Backwards compatibility:** runs without tools send no new fields. `Requires` guards against older runners from this release on.

### P5: `placement: sidecar` with `none`, `sidecar` and `sidecar-proxied` (S2, S5)
- [x] `promptrun.bindInlineTools`: only supervisor tools are bound locally. `checkSidecarTools` refuses `ask` and `direct` sidecar tools and records each tool's resolved credentials mode on the spec. The adapter ships the declarations with grant header values stripped (`withoutGrantValues`).
- [x] `run-task` builds an `exectool.Workspace` (cwd = worktree, `PrivateRoot` = `<task>/tools`) and calls `Prepare` before the agent starts:
  - **`none`:** static configuration only.
  - **`sidecar`:** the tool's own `envVars`, `dotenv` and `connections`, resolved on the sidecar.
  - **`sidecar-proxied`:** `ReleaseGrants` fetches `creds/<tool>` once (`FetchGrantRelease` checks it against the declaration), then `ServeEgressProxy` runs with the values in memory.
- [x] Supervisor: `exectool.ServeGrantRelease`, a single-use, bearer-protected release for each `sidecar-proxied` tool.
- [x] The tools dir is in the agent's deny-read list.
- [ ] Lint warning for `sidecar`/`sidecar-proxied` with an unconfined agent. Not done; run-task forces native confinement, so only Codex is affected, and it fails loudly at launch.
- [x] Tests: `exectool/sidecar_ginkgo_test.go`, `release_ginkgo_test.go`, promptrun preflight refusals, and e2e `TestDispatchRunsSidecarToolsBesideTheAgent` (placeholder, authenticated proxy, no grant value anywhere on the sidecar). The e2e's prompt lives outside the repo, because the dispatch ships the dirty worktree.
- **Backwards compatibility:** new modes only.

### P6: `proxied` for sidecar tools, and docs
- [x] `remoteExecProvider` runs `exectool.ServeToolProxies` for each `proxied` sidecar tool. The adapter registers `egress/<tool>` and `kube/<tool>` and ships `TaskSidecarTools.Proxies` (service names, egress secret, CA certificate, kube token), with the tool's connections stripped. `run-task` forwards both services (`forwardToolProxies`) and the workspace's `SupervisorProxies` points `ProxiedEnv` at them.
- [x] Tests: `exectool/sidecar_ginkgo_test.go` (kube through the supervisor's proxy, nothing inherited, relay closed with the environment); `adapter/gitagent_sidecar_tools_test.go` (services, payload free of grant values and the kubeconfig); promptrun records resolved modes; e2e `TestDispatchProxiesSidecarToolsThroughTheSupervisor`.
- [ ] S7 lint: warn on a grant with no `methods` or with a catch-all path on a `readonly` tool. Not done.
- [x] `format.mdx`: placements per sandbox, the credential matrix, "what each mode protects against", and same-release upgrades. The tunnel diagram was left out.
- [ ] Update `docs/plans/inline-tool-environment.md`. Not done.
- [ ] File TODOs for:
  - running the agent under a separate user or in a container on the sidecar (S2);
  - git-agent's unimplemented `ProvidesEgressProxy` claim for the agent's own traffic.

## Critical files
- `pkg/api/registry/{sandbox.go,sandboxes.go}`
- `pkg/api/{sandbox.go,inline_tool_environment.go}`
- `pkg/gitagent/{tunnel.go (new),server.go,httpserver.go,workspace.go,dispatch.go}`
- `pkg/cli/{serve_git.go,ai_sandbox_remote.go,gitagent_runtask.go}`
- `pkg/sandbox/adapter/{gitagent.go,srt.go}`
- `pkg/promptrun/{inline_tools.go,preflight_callertools.go}`
- `pkg/ai/tools/exectool/{proxied.go,workspace.go}`
- `pkg/credproxy/{proxy.go,connect.go,kubeproxy/proxy.go}`
- `pkg/ai/prompt/validate.go`

Reused as-is: `callertools.New` / `StartCLI`, the relay `TransportTarget` and clients (`sshclient.go`, `httpclient.go`), `credproxy.Authority`/`SandboxEnv`, the `exectool.Workspace` lifecycle, and commons-db `shell.SetupEnv` for `sidecar` resolution.

## Verification
1. `go test ./pkg/api/... ./pkg/credproxy/... ./pkg/gitagent/... ./pkg/ai/tools/exectool/... ./pkg/ai/prompt/... ./pkg/sandbox/...`. Then `pkg/promptrun` and the `pkg/cli` git-agent e2e tests with the sandbox disabled.
2. `make build`, and `make lint` on the touched packages.
3. Live smoke: local mailbox and sidecar `git-agent serve` processes, over SSH and then over HTTPS, running `hack/inline-tool-gitagent.prompt`. That prompt has four tools:
   - a supervisor tool writing a marker file;
   - a `sidecar` tool echoing a sidecar-only variable;
   - a `sidecar-proxied` httpbin `/headers` tool;
   - a `proxied` sidecar httpbin tool.

   Pass criteria:
   - all four tools succeed;
   - `rg` finds no secret value in the agent's worktree, `task.json`, `agent.*.log` or the agent's environment dump;
   - a direct `curl` from the agent to a proxy without the secret gets 407.
