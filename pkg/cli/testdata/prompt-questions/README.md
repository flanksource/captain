# Prompt question regressions

These fixtures exercise the real Claude Agent SDK and Codex app-server bridges through `captain prompt run`, with every model response supplied by `captain ai mock`. They cover terminal question routing, answer delivery, and the no-question policy without Ginkgo.

Run from the repository root:

```sh
make build
gavel fixtures outline 'pkg/cli/testdata/prompt-questions/*.fixture.yaml'
gavel fixtures --max-retries 0 --json 'pkg/cli/testdata/prompt-questions/*.fixture.yaml'
```

Dependencies: Gavel with YAML services and scripted PTY input, Node/npm, the Claude Agent SDK runtime, Codex CLI, jq, and psql for the workflow recording assertion. The workflow fixture provisions an isolated commons-db test database and applies Captain's migration bundle. It never uses the monitored session database.

Each document starts its own strict loopback mock on an allocated port and creates a workspace, Claude configuration directory, and request journal under `.tmp/prompt-questions-<case>.*`. Mock services are reaped by Gavel; workspace evidence remains available after a failure. Every case disables Captain monitoring hooks, and cases outside the workflow fixture also disable session recording.

| Fixture | Regression |
| --- | --- |
| `streamed.fixture.yaml` | A native question reaches the terminal broker and the selected answer reaches the model. |
| `buffered.fixture.yaml` | `--no-stream` still permits questions. |
| `codex.fixture.yaml` | Codex plan mode asks through the broker, while a run without a terminal disables its question tool. |
| `redirected-stdout.fixture.yaml` | Questions use stderr while stdout remains a JSON command result. |
| `noninteractive.fixture.yaml` | Piped input removes native questions and supplies an explicit no-question instruction. |
| `terminal-policy.fixture.yaml` | Redirected stderr and explicit Claude bypass both disable questions without changing permission mode. |
| `workflow.fixture.yaml` | Workflow execution asks, resumes, verifies, and records the run as succeeded. |

The scripted question offers both postgres and sqlite. The journal assertion checks the exact question-to-answer mapping in the tool result, so sqlite appearing among the original options is insufficient. The noninteractive assertion checks the advertised tools and system instructions sent to the mock; its scripted final reply alone is not evidence of policy enforcement.

A missing terminal form is a regression even if the mock subsequently supplies a final response to the tool's error. Missing dependencies, mock request misses, and database startup errors are harness failures and must be reported separately.
