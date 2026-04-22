ABOUTME: Mission Control MCP vs direct kubectl/AWS benchmark fixtures.
ABOUTME: Used with `captain ai fixture -f <file>` to produce evidence-quality comparisons.

# AI fixtures

YAML fixtures for `captain ai fixture`. Each file pits the Mission Control
MCP against direct Bash access to the same cluster/cloud, on the same prompt.

Run one fixture:

```bash
captain ai fixture -f examples/ai-fixtures/mission-control-investigate.yaml
```

Write a markdown evidence report as well:

```bash
captain ai fixture \
  -f examples/ai-fixtures/mission-control-investigate.yaml \
  --report /tmp/mc-investigate.md
```

Override the repeat count (e.g. smoke test):

```bash
captain ai fixture -f examples/ai-fixtures/mission-control-describe.yaml --repeat 1
```

## Fixtures

| File | What it measures |
|---|---|
| `mission-control-investigate.yaml` | Open-ended incident triage. Three runs: direct Bash, MCP with caching, MCP without caching. |
| `mission-control-describe.yaml` | Single read-heavy lookup — best case for MCP structured access. |
| `mission-control-multistep.yaml` | Find → inspect → tail chain — highlights MCP turn reduction. |

## Prerequisites

- `claude` CLI installed and authenticated.
- For the MCP runs: a `.mcp.json` in the same directory as the fixture, pointing
  at a reachable Mission Control MCP server.
- For the direct runs: `kubectl` and `aws` configured against the target
  environment. These fixtures assume `bypassPermissions` — run them against a
  read-only context.
