---
exec: bash
args: ["-c", "captain history -f $PWD/history.jsonl --since now-10y {{.flags}}"]
flags: ""
---

# Captain History Output Modes

## Default output (piped)

| Name | flags | CEL Validation |
|------|-------|----------------|
| shows all tool types | | stdout.contains("Bash") && stdout.contains("Read") && stdout.contains("Edit") && stdout.contains("Write") && stdout.contains("Grep") |
| shows denied reason | | stdout.contains("too dangerous") |
| shows ask tool | | stdout.contains("ask") |

## Table output

| Name | flags | CEL Validation |
|------|-------|----------------|
| table has border chars | --table | stdout.contains("│") |
| table has columns | --table | stdout.contains("Time") && stdout.contains("Tool") && stdout.contains("Subject") |
| table has pretty headers | --table | stdout.contains("bash") && stdout.contains("read") && stdout.contains("edit") |
| table shows all tools | --table | stdout.contains("Bash") && stdout.contains("Read") && stdout.contains("Edit") |

## JSON output

| Name | flags | CEL Validation |
|------|-------|----------------|
| json has 9 results | --json | json.results.size() == 9 |
| json has Bash tool | --json | json.results.exists(r, r.tool == "Bash") |
| json summary has command | --json | json.results.exists(r, r.summary.contains("bash")) |
| json has denied Bash | --json | json.results.exists(r, r.tool == "Bash" && r.approved == "✗") |
| json has Ask tool | --json | json.results.exists(r, r.tool == "Ask") |
| json has time field | --json | json.results.exists(r, r.time != "") |
| json has category | --json | json.results.exists(r, r.category != "") |
| json has User rows | --json | json.results.exists(r, r.tool == "User") |
| json user has text | --json | json.results.exists(r, r.tool == "User" && r.summary.contains("yes please")) |
| json no ansi escapes | --json | !stdout.contains("\u001b") |

## Summary output

| Name | flags | CEL Validation |
|------|-------|----------------|
| summary has label | --summary | stdout.contains("Total Tool Uses") |
| summary json count | --summary --json | json.totalToolUses == 9 |
| summary json denied | --summary --json | json.deniedCount == 1 |
| summary json has Bash | --summary --json | json.tools.exists(t, t.name == "Bash") |
| summary json has categories | --summary --json | json.categories.size() > 0 |

## Filtering

| Name | flags | CEL Validation |
|------|-------|----------------|
| filter tool Read | --json -t Read | json.results.size() == 1 && json.results[0].tool == "Read" |
| filter denied only | --json --approved false | json.results.size() == 1 |
| filter approved only | --json --approved true | json.results.size() == 8 |
| limit to 3 | --json -l 3 | json.results.size() == 3 |
